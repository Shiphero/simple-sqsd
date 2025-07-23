package supervisor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	log "github.com/sirupsen/logrus"
)

type Supervisor struct {
	sync.Mutex

	logger        *log.Entry
	sqs           sqsiface.SQSAPI
	httpClient    httpClient
	workerConfig  WorkerConfig
	hmacSignature string

	startOnce sync.Once
	wg        sync.WaitGroup

	shutdown bool
}

type WorkerConfig struct {
	QueueURL         string
	QueueMaxMessages int
	QueueWaitTime    int

	HTTPURL         string
	HTTPContentType string

	HTTPAUTHORIZATIONHeader     string
	HTTPAUTHORIZATIONHeaderName string

	HTTPHMACHeader string
	HMACSecretKey  []byte

	UserAgent string

	ErrorVisibilityTimeout int

	// These configs and the reference values come from real world examples of SQSD
	// VisibilityTimeout is The amount of time to lock an incoming message for processing before returning it to the queue.
	VisibilityTimeout int
	// MaxRetries is the Maximum number of retries after which the message is discarded.
	MaxRetries int // Default 10
	// InactivityTimeout is the Number of seconds to wait for a response from the application on an existing connection.
	InactivityTimeoutSeconds int // Default 815
	// RetentionPeriod is the Number of seconds that a message is valid for active processing.
	RetentionPeriodSeconds int // Default 345600
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewSupervisor(logger *log.Entry, sqs sqsiface.SQSAPI, httpClient httpClient, config WorkerConfig) *Supervisor {
	return &Supervisor{
		logger:        logger,
		sqs:           sqs,
		httpClient:    httpClient,
		workerConfig:  config,
		hmacSignature: fmt.Sprintf("POST %s\n", config.HTTPURL),
	}
}

func (s *Supervisor) Start(numWorkers int) {
	s.startOnce.Do(func() {
		s.wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go s.worker()
		}
	})
}

func (s *Supervisor) Wait() {
	s.wg.Wait()
}

func (s *Supervisor) Shutdown() {
	defer s.Unlock()
	s.Lock()

	s.shutdown = true
}

const maxDurationStat = 100

// min diff to detect in milliseconds for processing time trend
const minTrendUpWeCareFor = 1000.0 // ms

func (s *Supervisor) worker() {
	defer s.wg.Done()

	s.logger.Info("Starting worker")
	var lastNSQSRecvTimes = make([]float64, 0, maxDurationStat)

	for {
		if s.shutdown {
			return
		}

		begin := time.Now()
		recInput := &sqs.ReceiveMessageInput{
			MaxNumberOfMessages:   aws.Int64(int64(s.workerConfig.QueueMaxMessages)),
			QueueUrl:              aws.String(s.workerConfig.QueueURL),
			WaitTimeSeconds:       aws.Int64(int64(s.workerConfig.QueueWaitTime)),
			VisibilityTimeout:     aws.Int64(int64(s.workerConfig.VisibilityTimeout)),
			MessageAttributeNames: aws.StringSlice([]string{"All"}),
		}

		output, err := s.sqs.ReceiveMessage(recInput)
		if err != nil {
			s.logger.Errorf("Error while receiving messages from the queue: %s", err)
			continue
		}
		recvMsgDeltaT := time.Now().Sub(begin)
		s.logger.Debugf("receiving from SQS took %f seconds", recvMsgDeltaT.Seconds())
		if len(lastNSQSRecvTimes) == maxDurationStat {
			// shift left
			lastNSQSRecvTimes = lastNSQSRecvTimes[1:]
		}
		lastNSQSRecvTimes = append(lastNSQSRecvTimes, float64(recvMsgDeltaT.Milliseconds()))
		var epsilon = math.NaN()
		if n := len(lastNSQSRecvTimes); n >= 2 {
			epsilon = minTrendUpWeCareFor / float64(n-1) // ms por paso
		}
		if Trend(lastNSQSRecvTimes, epsilon) > 0 {
			ms := slices.Max(lastNSQSRecvTimes) / 1000.0
			s.logger.Warnf("Time for consuming sqs msgs going up for %d msgs, worse time is %f seconds", len(lastNSQSRecvTimes), ms)
		}

		if len(output.Messages) == 0 {
			continue
		}

		deleteEntries := make([]*sqs.DeleteMessageBatchRequestEntry, 0)
		changeVisibilityEntries := make([]*sqs.ChangeMessageVisibilityBatchRequestEntry, 0)

		batchTimes := make([]float64, 0, len(output.Messages))
		for _, msg := range output.Messages {
			// Perform a check on the message's validity and expire it if deemed too old.
			if firstReceivedTSInSecondsStr, ok := msg.Attributes["ApproximateFirstReceiveTimestamp"]; ok && firstReceivedTSInSecondsStr != nil {
				var firstReceivedTSInSeconds int64
				if firstReceivedTSInSeconds, err = strconv.ParseInt(*firstReceivedTSInSecondsStr, 10, 64); err != nil {
					s.logger.Errorf("Error while parsing first received timestamp (%s): %s", *firstReceivedTSInSecondsStr, err)
				}
				if nowUnix := time.Now().UTC().Unix(); nowUnix > firstReceivedTSInSeconds+int64(s.workerConfig.RetentionPeriodSeconds) {
					sinceFirstSeen := nowUnix - firstReceivedTSInSeconds
					s.logger.Debugf("Expired Message %d was first received %d seconds ago, deleting", msg.MessageId, sinceFirstSeen)
					deleteEntries = append(deleteEntries, &sqs.DeleteMessageBatchRequestEntry{
						Id:            msg.MessageId,
						ReceiptHandle: msg.ReceiptHandle,
					})
					continue
				}
			}

			msgProcessBegin := time.Now()
			res, err := s.httpRequest(msg)
			if err != nil {
				s.logger.Errorf("Error making HTTP request: %s", err)
				if s.workerConfig.ErrorVisibilityTimeout > 0 {
					changeVisibilityEntries = append(changeVisibilityEntries, &sqs.ChangeMessageVisibilityBatchRequestEntry{
						Id:                msg.MessageId,
						ReceiptHandle:     msg.ReceiptHandle,
						VisibilityTimeout: aws.Int64(int64(s.workerConfig.ErrorVisibilityTimeout)),
					})
				}
				continue
			}

			if res.StatusCode < http.StatusOK || res.StatusCode > http.StatusIMUsed {

				switch res.StatusCode {
				case http.StatusTooManyRequests:
					sec, err := getRetryAfterFromResponse(res)
					if err != nil {
						s.logger.Errorf("Error getting retry after value from HTTP response: %s", err)
						continue
					}

					changeVisibilityEntries = append(changeVisibilityEntries, &sqs.ChangeMessageVisibilityBatchRequestEntry{
						Id:                msg.MessageId,
						ReceiptHandle:     msg.ReceiptHandle,
						VisibilityTimeout: aws.Int64(sec),
					})
				default:
					if s.workerConfig.ErrorVisibilityTimeout > 0 {
						changeVisibilityEntries = append(changeVisibilityEntries, &sqs.ChangeMessageVisibilityBatchRequestEntry{
							Id:                msg.MessageId,
							ReceiptHandle:     msg.ReceiptHandle,
							VisibilityTimeout: aws.Int64(int64(s.workerConfig.ErrorVisibilityTimeout)),
						})
					}
				}

				continue

			}
			msgProcessDeltaT := time.Now().Sub(msgProcessBegin)
			s.logger.Debugf("Processed message %d in %f seconds", msg.MessageId, msgProcessDeltaT.Seconds())
			batchTimes = append(batchTimes, float64(msgProcessDeltaT.Milliseconds()))

			deleteEntries = append(deleteEntries, &sqs.DeleteMessageBatchRequestEntry{
				Id:            msg.MessageId,
				ReceiptHandle: msg.ReceiptHandle,
			})

			s.logger.Debugf("Message %s successfully processed", *msg.MessageId)
		}

		var epsilonBatch = math.NaN()
		if n := len(batchTimes); n >= 2 {
			epsilonBatch = minTrendUpWeCareFor / float64(n-1) // ms por paso
		}
		if Trend(batchTimes, epsilonBatch) > 0 {
			ms := slices.Max(batchTimes) / 1000.0
			s.logger.Warnf("Time for processing sqs msgs going up for %d msgs, worse time is %f seconds", len(batchTimes), ms)
		}

		if len(deleteEntries) > 0 {
			delInput := &sqs.DeleteMessageBatchInput{
				Entries:  deleteEntries,
				QueueUrl: aws.String(s.workerConfig.QueueURL),
			}

			_, err = s.sqs.DeleteMessageBatch(delInput)
			if err != nil {
				s.logger.Errorf("Error while deleting messages from SQS: %s", err)
			}
		}

		if len(changeVisibilityEntries) > 0 {
			changeVisibilityInput := &sqs.ChangeMessageVisibilityBatchInput{
				Entries:  changeVisibilityEntries,
				QueueUrl: aws.String(s.workerConfig.QueueURL),
			}

			_, err = s.sqs.ChangeMessageVisibilityBatch(changeVisibilityInput)
			if err != nil {
				s.logger.Errorf("Error while changing visibility on messages from SQS: %s", err)
			}
		}
	}
}

func (s *Supervisor) httpRequest(msg *sqs.Message) (*http.Response, error) {
	body := *msg.Body
	req, err := http.NewRequest("POST", s.workerConfig.HTTPURL, bytes.NewBufferString(body))
	if err != nil {
		return nil, fmt.Errorf("Error while creating HTTP request: %s", err)
	}
	req.Header.Add("X-Aws-Sqsd-Msgid", *msg.MessageId)
	s.addMessageAttributesToHeader(msg.MessageAttributes, req.Header)

	if len(s.workerConfig.HMACSecretKey) > 0 {
		hmac, err := makeHMAC(strings.Join([]string{s.hmacSignature, body}, ""), s.workerConfig.HMACSecretKey)
		if err != nil {
			return nil, err
		}

		req.Header.Set(s.workerConfig.HTTPHMACHeader, hmac)
	}

	if len(s.workerConfig.HTTPAUTHORIZATIONHeader) > 0 {
		headerName := s.workerConfig.HTTPAUTHORIZATIONHeaderName
		if len(headerName) == 0 {
			headerName = "Authorization"
		}
		req.Header.Set(headerName, s.workerConfig.HTTPAUTHORIZATIONHeader)
	}

	if len(s.workerConfig.HTTPContentType) > 0 {
		req.Header.Set("Content-Type", s.workerConfig.HTTPContentType)
	}

	if len(s.workerConfig.UserAgent) > 0 {
		req.Header.Set("User-Agent", s.workerConfig.UserAgent)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return res, err
	}

	res.Body.Close()

	return res, nil
}

// sQSSkipAttrRe matching headers should not be forwarded
var sQSSkipAttrRe = regexp.MustCompile(`^beanstalk.sqsd.*`)

func (s *Supervisor) addMessageAttributesToHeader(attrs map[string]*sqs.MessageAttributeValue, header http.Header) {
	for k, v := range attrs {
		if sQSSkipAttrRe.Match([]byte(k)) {
			continue
		}
		if v.DataType != nil {
			lowerDT := strings.ToLower(*v.DataType)
			if !(strings.HasPrefix(lowerDT, "number") || strings.HasPrefix(lowerDT, "string")) {
				continue
			}
		}
		header.Add("X-Aws-Sqsd-Attr-"+k, *v.StringValue)
	}
}

func makeHMAC(signature string, secretKey []byte) (string, error) {
	mac := hmac.New(sha256.New, secretKey)

	_, err := mac.Write([]byte(signature))
	if err != nil {
		return "", fmt.Errorf("Error while writing HMAC: %s", err)
	}

	return hex.EncodeToString(mac.Sum(nil)), nil
}

func getRetryAfterFromResponse(res *http.Response) (int64, error) {
	retryAfter := res.Header.Get("Retry-After")
	if len(retryAfter) == 0 {
		return 0, errors.New("Retry-After header value is empty")
	}

	seconds, err := strconv.ParseInt(retryAfter, 10, 0)
	if err != nil {
		return 0, err
	}

	return max(seconds, 0), nil
}

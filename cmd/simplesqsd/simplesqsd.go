package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/fterrag/simple-sqsd/cron_worker"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/fterrag/simple-sqsd/supervisor"
	log "github.com/sirupsen/logrus"
)

type config struct {
	QueueRegion      string
	QueueURL         string
	QueueMaxMessages int
	QueueWaitTime    int

	// HTTPMaxConns is the Maximum number of concurrent connections to the application.
	HTTPMaxConns    int
	HTTPURL         string
	HTTPContentType string
	HTTPTimeout     int

	AWSEndpoint                 string
	HTTPHMACHeader              string
	HTTPAUTHORIZATIONHeader     string
	HTTPAUTHORIZATIONHeaderName string
	HMACSecretKey               []byte

	HTTPHealthPath        string
	HTTPHealthWait        int
	HTTPHealthInterval    int
	HTTPHealthSucessCount int

	SQSHTTPTimeout int
	SSLVerify      bool

	CronFile     string
	CronEndPoint string
	CronTimeout  int

	UserAgent string

	ErrorVisibilityTimeout int

	// VisibilityTimeout is The amount of time to lock an incoming message for processing before returning it to the queue.
	VisibilityTimeout int
	// MaxRetries is the Maximum number of retries after which the message is discarded.
	MaxRetries int // Default 10
	// InactivityTimeout is the Number of seconds to wait for a response from the application on an existing connection.
	InactivityTimeoutSeconds int // Default 815
	// RetentionPeriod is the Number of seconds that a message is valid for active processing.
	RetentionPeriodSeconds int // Default 345600
}

func main() {

	c := &config{}

	c.QueueRegion = os.Getenv("SQSD_QUEUE_REGION")
	c.QueueURL = os.Getenv("SQSD_QUEUE_URL")
	c.QueueMaxMessages = getEnvInt("SQSD_QUEUE_MAX_MSGS", 10)
	c.QueueWaitTime = getEnvInt("SQSD_QUEUE_WAIT_TIME", 10)

	c.HTTPMaxConns = getEnvInt("SQSD_HTTP_MAX_CONNS", 25)
	c.HTTPURL = os.Getenv("SQSD_HTTP_URL")
	c.HTTPContentType = os.Getenv("SQSD_HTTP_CONTENT_TYPE")
	c.UserAgent = os.Getenv("SQSD_HTTP_USER_AGENT")

	c.VisibilityTimeout = getEnvInt("SQSD_QUEUE_VISIBILITY_TIMEOUT", 820)
	c.MaxRetries = getEnvInt("SQSD_QUEUE_MAX_RETRIES", 10)
	c.InactivityTimeoutSeconds = getEnvInt("SQSD_QUEUE_INACTIVITY_TIMEOUT", 815)
	c.RetentionPeriodSeconds = getEnvInt("SQSD_RETENTION_PERIOD", 345600)

	c.HTTPHealthPath = os.Getenv("SQSD_HTTP_HEALTH_PATH")
	c.HTTPHealthWait = getEnvInt("SQSD_HTTP_HEALTH_WAIT", 5)
	c.HTTPHealthInterval = getEnvInt("SQSD_HTTP_HEALTH_INTERVAL", 5)
	c.HTTPHealthSucessCount = getEnvInt("SQSD_HTTP_HEALTH_SUCCESS_COUNT", 1)
	c.HTTPTimeout = getEnvInt("SQSD_HTTP_TIMEOUT", 15)

	c.AWSEndpoint = os.Getenv("SQSD_AWS_ENDPOINT")
	c.HTTPHMACHeader = os.Getenv("SQSD_HTTP_HMAC_HEADER")
	c.HTTPAUTHORIZATIONHeader = os.Getenv("SQSD_HTTP_AUTHORIZATION_HEADER")
	c.HTTPAUTHORIZATIONHeaderName = os.Getenv("SQSD_HTTP_AUTHORIZATION_HEADER_NAME")
	c.HMACSecretKey = []byte(os.Getenv("SQSD_HMAC_SECRET_KEY"))

	c.SQSHTTPTimeout = getEnvInt("SQSD_SQS_HTTP_TIMEOUT", 15)
	c.SSLVerify = getenvBool("SQSD_HTTP_SSL_VERIFY", true)

	c.CronFile = os.Getenv("SQSD_CRON_FILE")
	c.CronEndPoint = os.Getenv("SQSD_CRON_ENDPOINT")
	c.CronTimeout = getEnvInt("SQSD_CRON_TIMEOUT", 15)

	c.ErrorVisibilityTimeout = getEnvInt("SQSD_ERROR_VISIBILITY_TIMEOUT", 0)

	if len(c.QueueRegion) == 0 {
		log.Fatal("SQSD_QUEUE_REGION cannot be empty")
	}

	if len(c.QueueURL) == 0 {
		log.Fatal("SQSD_QUEUE_URL cannot be empty")
	}

	if len(c.HTTPURL) == 0 {
		log.Fatal("SQSD_HTTP_URL cannot be empty")
	}

	log.SetFormatter(&log.JSONFormatter{})

	logLevel := os.Getenv("LOG_LEVEL")
	if len(logLevel) == 0 {
		logLevel = "info"
	}
	if parsedLevel, err := log.ParseLevel(logLevel); err == nil {
		log.SetLevel(parsedLevel)
	} else {
		log.Fatal(err)
	}

	logger := log.WithFields(log.Fields{
		"queueRegion":  c.QueueRegion,
		"queueUrl":     c.QueueURL,
		"httpMaxConns": c.HTTPMaxConns,
		"httpPath":     c.HTTPURL,
	})

	if len(c.HTTPHealthPath) != 0 {
		numSuccesses := 0
		healthURL := fmt.Sprintf("%s%s", c.HTTPURL, c.HTTPHealthPath)
		log.Infof("Waiting %d seconds before staring health check at '%s'", c.HTTPHealthWait, healthURL)
		time.Sleep(time.Duration(c.HTTPHealthWait) * time.Second)
		for {
			if resp, err := http.Get(healthURL); err == nil {
				log.Infof("%#v", resp)
				if numSuccesses == c.HTTPHealthSucessCount {
					break
				} else {
					numSuccesses++
				}
			} else {
				log.Debugf("Health check failed: %s. Waiting for %d seconds before next attempt", err, c.HTTPHealthInterval)
				time.Sleep(time.Duration(c.HTTPHealthInterval) * time.Second)
			}
		}
		log.Info("Health check succeeded. Starting message processing")
	}

	awsSess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))

	sqsHttpClient := &http.Client{
		Timeout: time.Duration(c.SQSHTTPTimeout) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        c.HTTPMaxConns,
			MaxIdleConnsPerHost: c.HTTPMaxConns,
		},
	}
	sqsConfig := aws.NewConfig().
		WithRegion(c.QueueRegion).
		WithHTTPClient(sqsHttpClient)

	if len(c.AWSEndpoint) > 0 {
		sqsConfig.WithEndpoint(c.AWSEndpoint)
	}

	sqsSvc := sqs.New(awsSess, sqsConfig)

	wConf := supervisor.WorkerConfig{
		QueueURL:         c.QueueURL,
		QueueMaxMessages: c.QueueMaxMessages,
		QueueWaitTime:    c.QueueWaitTime,

		HTTPURL:         c.HTTPURL,
		HTTPContentType: c.HTTPContentType,

		HTTPAUTHORIZATIONHeader:     c.HTTPAUTHORIZATIONHeader,
		HTTPAUTHORIZATIONHeaderName: c.HTTPAUTHORIZATIONHeaderName,

		HTTPHMACHeader: c.HTTPHMACHeader,
		HMACSecretKey:  c.HMACSecretKey,

		UserAgent: c.UserAgent,

		ErrorVisibilityTimeout: c.ErrorVisibilityTimeout,

		MaxRetries:               c.MaxRetries,
		InactivityTimeoutSeconds: c.InactivityTimeoutSeconds,
		RetentionPeriodSeconds:   c.RetentionPeriodSeconds,
		VisibilityTimeout:        c.VisibilityTimeout,
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        c.HTTPMaxConns,
			MaxIdleConnsPerHost: c.HTTPMaxConns,
			TLSClientConfig: &tls.Config{
				MaxVersion:         tls.VersionTLS11,
				InsecureSkipVerify: !c.SSLVerify,
			},
		},
		Timeout: time.Duration(c.HTTPTimeout) * time.Second,
	}

	if "" == c.CronEndPoint {
		parts, err := url.Parse(c.HTTPURL)
		if nil == err {
			parts.RawQuery = ""
			parts.Path = ""
			parts.Fragment = ""
			c.CronEndPoint = parts.String()
		}
	}
	if "" != c.CronFile && "" == c.CronEndPoint {
		log.Fatal("You need to specify SQSD_CRON_URL")
	}
	cronDaemon := cron_worker.New(&cron_worker.Config{
		File:                        c.CronFile,
		EndPoint:                    c.CronEndPoint,
		Timeout:                     time.Duration(c.CronTimeout) * time.Second,
		UserAgent:                   c.UserAgent,
		HTTPContentType:             c.HTTPContentType,
		HTTPAUTHORIZATIONHeader:     c.HTTPAUTHORIZATIONHeader,
		HTTPAUTHORIZATIONHeaderName: c.HTTPAUTHORIZATIONHeaderName,
	})
	if cronDaemon != nil {
		go cronDaemon.Run()
		defer cronDaemon.Stop()
	}

	s := supervisor.NewSupervisor(logger, sqsSvc, httpClient, wConf)
	s.Start(c.HTTPMaxConns)
	s.Wait()
}

func getEnvInt(key string, def int) int {
	val, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}

	return val
}

var ErrEnvVarEmpty = errors.New("getenv: environment variable empty")

func getenvStr(key string) (string, error) {
	v := os.Getenv(key)
	if len(v) == 0 {
		return v, ErrEnvVarEmpty
	}
	return v, nil
}

func getenvBool(key string, def bool) bool {
	s, err := getenvStr(key)
	if err != nil {
		return def
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return v
}

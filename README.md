[![Go Report Card](https://goreportcard.com/badge/github.com/fterrag/simple-sqsd)](https://goreportcard.com/report/github.com/fterrag/simple-sqsd)

# simple-sqsd

A simple version of the AWS Elastic Beanstalk Worker Environment SQS daemon ([sqsd](https://docs.aws.amazon.com/elasticbeanstalk/latest/dg/using-features-managing-env-tiers.html#worker-daemon)).

## Getting Started

```bash
$ SQSD_QUEUE_REGION=us-east-1 SQSD_QUEUE_URL=http://queue.url SQSD_HTTP_URL=http://service.url/endpoint go run cmd/simplesqsd/simplesqsd.go
```

Docker (uses a GitHub Container Registry):
```bash
$ docker run -e AWS_ACCESS_KEY_ID=your-access-id -e AWS_SECRET_ACCESS_KEY=your-secret-key -e SQSD_QUEUE_REGION=us-east-1 -e SQSD_QUEUE_URL=http://queue.url -e SQSD_HTTP_URL=http://service.url/endpoint ghcr.io/fterrag/simple-sqsd:latest
```

## Configuration

|**Environment Variable**| **Default Value**                  |**Required**| **Description**                                                                                                                                                               |
|-|------------------------------------|-|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
|`SQSD_QUEUE_REGION`|                                    |yes| The region of the SQS queue.                                                                                                                                                  |
|`SQSD_QUEUE_URL`|                                    |yes| The URL of the SQS queue.                                                                                                                                                     |
|`SQSD_QUEUE_MAX_MSGS`| `10`                               |no| Max number of messages a worker should try to receive from the SQS queue.                                                                                                     |
|`SQSD_QUEUE_WAIT_TIME`| `10`                               |no| The duration (in seconds) for which the call waits for a message to arrive in the queue before returning. Setting this to `0` disables long polling. Maximum of `20` seconds. |
|`SQSD_HTTP_MAX_CONNS`| `25`                               |no| Maximum number of concurrent HTTP requests to make to SQSD_HTTP_URL.                                                                                                          |
|`SQSD_HTTP_URL`|                                    |yes| The URL of your service to make a request to.                                                                                                                                 |
|`SQSD_HTTP_CONTENT_TYPE` |                                    |no| The value to send for the HTTP header `Content-Type` when making a request to your service.                                                                                   |
|`SQSD_HTTP_USER_AGENT`|                                    |no| The value to send for the HTTP header `User-Agent` when making a request to your service.                                                                                     |
|`SQSD_AWS_ENDPOINT` |                                    |no| Sets the AWS endpoint.                                                                                                                                                        |
|`SQSD_HTTP_HMAC_HEADER`|                                    |no| The name of the HTTP header to send the HMAC hash with.                                                                                                                       |
|`SQSD_HMAC_SECRET_KEY`|                                    |no| Secret key to use when generating HMAC hash send to `SQSD_HTTP_URL`.                                                                                                          |
|`SQSD_HTTP_HEALTH_PATH`|                                    |no| The path to a health check endpoint of your service. When provided, messages will not be processed until the health check returns a 200 for `HTTPHealthInterval` times        |
|`SQSD_HTTP_HEALTH_WAIT`| `5`                                |no| How long to wait before starting health checks                                                                                                                                |
|`SQSD_HTTP_HEALTH_INTERVAL`| `5`                                |no| How often to wait between health checks                                                                                                                                       |
|`SQSD_HTTP_HEALTH_SUCCESS_COUNT`| `1`                                |no| How many successful health checks required in a row                                                                                                                           |
|`SQSD_HTTP_TIMEOUT`| `15`                               |no| Number of seconds to wait for a response from the worker                                                                                                                      |
|`SQSD_SQS_HTTP_TIMEOUT`| `15`                               |no| Number of seconds to wait for a response from sqs                                                                                                                             |
|`SQSD_HTTP_SSL_VERIFY`| `true`                             |no| Enable SSL Verification on the URL of your service to make a request to (if you're using self-signed certificate)                                                             |
|`SQSD_HTTP_AUTHORIZATION_HEADER`|                                    |no| A simple feature to add a jwt/simple token to Authorization header for basic auth on SQSD_HTTP_URL                                                                            |
|`SQSD_HTTP_AUTHORIZATION_HEADER_NAME`|                                    |no| override the http header name (defaults to Authorization) in SQSD_HTTP_AUTHORIZATION_HEADER                                                                                   |
|`SQSD_CRON_FILE`|                                    |no| The elastic beanstalk cron.yaml file to load                                                                                                                                  |
|`SQSD_CRON_ENDPOINT`| `SQSD_HTTP_URL` without path/query |yes if SQSD_CRON_FILE| The base URL to call (e.g. http://localhost:3000). cron.yaml url will be appended to this                                                                                     |
|`SQSD_CRON_TIMEOUT`| `15`                               |no| Duration (in seconds) To wait for the cron endpoint to response                                                                                                               |
|`SQSD_QUEUE_VISIBILITY_TIMEOUT`| `820`                              |no| Duration (in seconds) To lock an incoming message for processing before returning it to the queue.                                                                            |
|`SQSD_QUEUE_MAX_RETRIES`| `10`                               |no| Maximum number of retries after which the message is discarded                                                                                                                |
|`SQSD_QUEUE_INACTIVITY_TIMEOUT`| `815`                              |no| Duration (in seconds) To wait for a response from the application on an existing connection.                                                                                  |
|`SQSD_RETENTION_PERIOD`| `345600`                              |no| Duration (in seconds) that a message is valid for active processing                                                                                                           |



## HMAC

*Optionally* (when SQSD_HTTP_HMAC_HEADER and SQSD_HMAC_SECRET_KEY are set), HMAC hashes are generated using SHA-256 with the signature made up of the following:
```
POST {SQSD_HTTP_URL}\n
<SQS message body>
```

## Support 429 Status codes with Retry-After

* SQSD will attempt to change the message visibility when the service responds with [429 status code](https://tools.ietf.org/html/rfc6585#section-4).
* `Retry-After` response header should contain an integer with the amount of senconds to wait.

## Todo
- [ ] More Tests
- [ ] Documentation

## Contributing

* Submit a PR
* Add or improve documentation
* Report issues
* Suggest new features or enhancements

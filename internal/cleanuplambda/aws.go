package cleanuplambda

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/JosephWest2/cloud_dev/internal/expirycleanup"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type Logs interface {
	PutLogEvents(context.Context, *cloudwatchlogs.PutLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error)
}
type AWSJournal struct {
	Client                   Logs
	Group, Stream, RequestID string
	Scope                    expiry.Scope
	Input                    expiry.ScheduledInput
	Clock                    expiry.Clock
}

func (j *AWSJournal) Emit(ctx context.Context, e expiry.Event) error {
	return j.Record(ctx, Record{SchemaVersion: 1, Kind: e.Kind, RequestID: j.RequestID, Scope: j.Scope, Correlation: j.Input, EmittedAt: e.EmittedAt, Event: &e})
}
func (j *AWSJournal) Record(ctx context.Context, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return errors.New("cleanup_evidence_unavailable")
	}
	// Never truncate a root mapping or summary and report successful persistence.
	if len(data) > 1000000 {
		return errors.New("cleanup_evidence_too_large")
	}
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := j.Client.PutLogEvents(request, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(j.Group), LogStreamName: aws.String(j.Stream), LogEvents: []types.InputLogEvent{{Message: aws.String(string(data)), Timestamp: aws.Int64(j.Clock.Now().UnixMilli())}}})
	if err != nil || request.Err() != nil || out == nil || out.RejectedLogEventsInfo != nil || out.RejectedEntityInfo != nil {
		return errors.New("cleanup_evidence_unavailable")
	}
	return nil
}

func AWSFactory(ctx context.Context, s Settings, requestID string, input expiry.ScheduledInput) (Runner, Journal, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(s.Scope.Region), config.WithRetryMaxAttempts(3))
	if err != nil {
		return nil, nil, err
	}
	logs := cloudwatchlogs.NewFromConfig(cfg)
	stream := "cleanup/" + requestID
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(s.LogGroup), LogStreamName: aws.String(stream)})
	var exists *types.ResourceAlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return nil, nil, err
	}
	j := &AWSJournal{Client: logs, Group: s.LogGroup, Stream: stream, RequestID: requestID, Scope: s.Scope, Input: input, Clock: expiry.SystemClock{}}
	runner, err := expirycleanup.NewAWS(s.Scope, cfg, expiry.SystemClock{}, j, expirycleanup.Limits{})
	return runner, j, err
}

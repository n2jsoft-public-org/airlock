package logsmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

const (
	// callTimeout bounds each tool call but query.
	callTimeout = 30 * time.Second
	// queryTimeout bounds the wait for a Logs Insights query, stopped past it.
	queryTimeout = 60 * time.Second
	// pollInterval spaces the GetQueryResults calls of a running query.
	pollInterval = time.Second
	// maxResultBytes caps the rows or events of a result: the oldest events, the last rows go past it.
	maxResultBytes = 256 << 10
	// maxValueBytes cuts a longer message or field value.
	maxValueBytes = 8 << 10
	defaultLimit  = 100
	// maxLimit caps the rows of a query (Logs Insights allows 10,000) and the events of filter_events.
	maxLimit = 1000
	// listPage is the number of groups or streams per page, the most DescribeLogGroups returns.
	listPage = 50
	// maxGroups is the most log groups a Logs Insights query takes.
	maxGroups = 50
	// eventsPage is the most events FilterLogEvents returns per call.
	eventsPage = 10000
	// truncatedSuffix ends a cut message or value.
	truncatedSuffix = "…<truncated>"
)

// api is the part of the CloudWatch Logs client the tools use; *cloudwatchlogs.Client implements it.
type api interface {
	DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
	DescribeLogStreams(context.Context, *cloudwatchlogs.DescribeLogStreamsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogStreamsOutput, error)
	StartQuery(context.Context, *cloudwatchlogs.StartQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error)
	FilterLogEvents(context.Context, *cloudwatchlogs.FilterLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
}

// logs runs the tool operations; now, poll and queryTimeout are fields so tests can shorten them.
type logs struct {
	api          api
	now          func() time.Time
	poll         time.Duration
	queryTimeout time.Duration
}

func newLogs(a api) *logs {
	return &logs{api: a, now: time.Now, poll: pollInterval, queryTimeout: queryTimeout}
}

// cut shortens s to maxValueBytes, reporting whether it did.
func cut(s string) (string, bool) {
	if len(s) <= maxValueBytes {
		return s, false
	}
	return strings.ToValidUTF8(s[:maxValueBytes], "") + truncatedSuffix, true
}

type listGroupsInput struct {
	Prefix  string `json:"prefix,omitempty" jsonschema:"only the groups whose name starts with this text, e.g. /aws/lambda/"`
	Pattern string `json:"pattern,omitempty" jsonschema:"only the groups whose name contains this text, ignoring case; not with prefix"`
	Start   string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type groupOutput struct {
	Name          string `json:"name"`
	Class         string `json:"class,omitempty"`
	Created       string `json:"created,omitempty"`
	RetentionDays int32  `json:"retention_days,omitempty" jsonschema:"days the events are kept; absent: forever"`
	StoredBytes   int64  `json:"stored_bytes"`
}

type listGroupsOutput struct {
	Next   string        `json:"next,omitempty"`
	Groups []groupOutput `json:"groups"`
}

func (l *logs) Groups(ctx context.Context, in *listGroupsInput) (listGroupsOutput, error) {
	out := listGroupsOutput{Groups: []groupOutput{}}
	if in.Prefix != "" && in.Pattern != "" {
		return out, errors.New("give prefix or pattern, not both")
	}
	req := &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(listPage)}
	if in.Prefix != "" {
		req.LogGroupNamePrefix = aws.String(in.Prefix)
	}
	if in.Pattern != "" {
		req.LogGroupNamePattern = aws.String(in.Pattern)
	}
	if in.Start != "" {
		req.NextToken = aws.String(in.Start)
	}
	res, err := l.api.DescribeLogGroups(ctx, req)
	if err != nil {
		return out, mcpserve.AWSError(err)
	}
	for idx := range res.LogGroups {
		g := &res.LogGroups[idx]
		out.Groups = append(out.Groups, groupOutput{
			Name: aws.ToString(g.LogGroupName), Class: string(g.LogGroupClass), Created: stamp(g.CreationTime),
			RetentionDays: aws.ToInt32(g.RetentionInDays), StoredBytes: aws.ToInt64(g.StoredBytes),
		})
	}
	out.Next = aws.ToString(res.NextToken)
	return out, nil
}

type listStreamsInput struct {
	Group string `json:"group" jsonschema:"the log group name"`
	Start string `json:"start,omitempty" jsonschema:"next of the previous page, to continue the listing"`
}

type streamOutput struct {
	Name       string `json:"name"`
	FirstEvent string `json:"first_event,omitempty"`
	LastEvent  string `json:"last_event,omitempty"`
}

type listStreamsOutput struct {
	Next    string         `json:"next,omitempty"`
	Streams []streamOutput `json:"streams"`
}

func (l *logs) Streams(ctx context.Context, in *listStreamsInput) (listStreamsOutput, error) {
	out := listStreamsOutput{Streams: []streamOutput{}}
	req := &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupIdentifier: aws.String(in.Group), OrderBy: types.OrderByLastEventTime, Descending: aws.Bool(true), Limit: aws.Int32(listPage),
	}
	if in.Start != "" {
		req.NextToken = aws.String(in.Start)
	}
	res, err := l.api.DescribeLogStreams(ctx, req)
	if err != nil {
		return out, mcpserve.AWSError(err)
	}
	for idx := range res.LogStreams {
		s := &res.LogStreams[idx]
		out.Streams = append(out.Streams, streamOutput{
			Name: aws.ToString(s.LogStreamName), FirstEvent: stamp(s.FirstEventTimestamp), LastEvent: stamp(s.LastEventTimestamp),
		})
	}
	out.Next = aws.ToString(res.NextToken)
	return out, nil
}

type queryInput struct {
	Query  string   `json:"query" jsonschema:"the Logs Insights query, e.g. fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc"`
	Start  string   `json:"start,omitempty" jsonschema:"start of the range: a duration before now (15m, 1h, 7d) or an RFC 3339 time; default 15m"`
	End    string   `json:"end,omitempty" jsonschema:"end of the range, in the same forms; default now"`
	Groups []string `json:"groups" jsonschema:"the log group names, 1 to 50"`
	Limit  int      `json:"limit,omitempty" jsonschema:"maximum rows, default 100, at most 1000"`
}

type statistics struct {
	RecordsMatched float64 `json:"records_matched"`
	RecordsScanned float64 `json:"records_scanned"`
	BytesScanned   float64 `json:"bytes_scanned" jsonschema:"what Logs Insights bills"`
}

type queryOutput struct {
	Rows       []map[string]string `json:"rows"`
	Statistics statistics          `json:"statistics"`
	Truncated  bool                `json:"truncated,omitempty" jsonschema:"rows were left out or values cut to fit the result"`
}

func (l *logs) Query(ctx context.Context, in *queryInput) (queryOutput, error) {
	out := queryOutput{Rows: []map[string]string{}}
	switch {
	case len(in.Groups) == 0 || len(in.Groups) > maxGroups:
		return out, fmt.Errorf("give 1 to %d log groups", maxGroups)
	case strings.TrimSpace(in.Query) == "":
		return out, errors.New("query is empty")
	}
	from, to, err := timeRange(in.Start, in.End, l.now())
	if err != nil {
		return out, err
	}
	limit := mcpserve.Limit(in.Limit, defaultLimit, maxLimit)
	started, err := l.api.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupIdentifiers: in.Groups, QueryString: aws.String(in.Query),
		StartTime: aws.Int64(from.Unix()), EndTime: aws.Int64(to.Unix()), Limit: aws.Int32(int32(limit)), //nolint:gosec // limit ≤ maxLimit.
	})
	if err != nil {
		return out, mcpserve.AWSError(err)
	}
	res, err := l.wait(ctx, started.QueryId)
	if err != nil {
		return out, err
	}
	if s := res.Statistics; s != nil {
		out.Statistics = statistics{RecordsMatched: s.RecordsMatched, RecordsScanned: s.RecordsScanned, BytesScanned: s.BytesScanned}
	}
	size := 0
	for _, fields := range res.Results {
		if len(out.Rows) == limit {
			out.Truncated = true
			break
		}
		row := make(map[string]string, len(fields))
		for _, f := range fields {
			name := aws.ToString(f.Field)
			if name == "@ptr" {
				continue // an opaque pointer to the event, of no use to a reader
			}
			value, cutValue := cut(aws.ToString(f.Value))
			out.Truncated = out.Truncated || cutValue
			row[name] = value
			size += len(name) + len(value)
		}
		if size > maxResultBytes {
			out.Truncated = true
			break
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// wait polls the results of query id until it ends, and stops it past l.queryTimeout.
func (l *logs) wait(ctx context.Context, id *string) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	waitCtx, cancel := context.WithTimeout(ctx, l.queryTimeout)
	defer cancel()
	for {
		res, err := l.api.GetQueryResults(waitCtx, &cloudwatchlogs.GetQueryResultsInput{QueryId: id})
		switch {
		case waitCtx.Err() != nil:
			return nil, l.stop(ctx, id)
		case err != nil:
			return nil, mcpserve.AWSError(err)
		case res.Status == types.QueryStatusComplete:
			return res, nil
		case res.Status != types.QueryStatusScheduled && res.Status != types.QueryStatusRunning:
			return nil, fmt.Errorf("the query ended %s", res.Status)
		}
		select {
		case <-waitCtx.Done():
			return nil, l.stop(ctx, id)
		case <-time.After(l.poll):
		}
	}
}

// stop stops query id, still billed for what it scanned, and says why.
func (l *logs) stop(ctx context.Context, id *string) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := l.api.StopQuery(stopCtx, &cloudwatchlogs.StopQueryInput{QueryId: id})
	reason := fmt.Errorf("the query did not complete within %s and was stopped: narrow the time range, the log groups or the query", l.queryTimeout)
	if err != nil {
		return errors.Join(reason, mcpserve.AWSError(err))
	}
	return reason
}

type filterInput struct {
	Group   string   `json:"group" jsonschema:"the log group name"`
	Pattern string   `json:"pattern,omitempty" jsonschema:"a CloudWatch Logs filter pattern, e.g. ERROR, \"timed out\" or { $.level = \"error\" }; empty: every event"`
	Start   string   `json:"start,omitempty" jsonschema:"start of the range: a duration before now (15m, 1h, 7d) or an RFC 3339 time; default 15m"`
	End     string   `json:"end,omitempty" jsonschema:"end of the range, in the same forms; default now"`
	Streams []string `json:"streams,omitempty" jsonschema:"only these log streams"`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum events, the newest ones; default 100, at most 1000"`
}

type eventOutput struct {
	Time    string `json:"time"`
	Stream  string `json:"stream"`
	Message string `json:"message"`
}

type filterOutput struct {
	Events    []eventOutput `json:"events" jsonschema:"the newest matching events, oldest first"`
	Truncated bool          `json:"truncated,omitempty" jsonschema:"older events were left out, or messages cut, to fit the limit"`
}

// Filter returns the newest events of the range matching the pattern: it reads every page, keeping
// the last ones, so a busy group needs a narrow range or a pattern.
func (l *logs) Filter(ctx context.Context, in *filterInput) (filterOutput, error) {
	out := filterOutput{Events: []eventOutput{}}
	from, to, err := timeRange(in.Start, in.End, l.now())
	if err != nil {
		return out, err
	}
	limit := mcpserve.Limit(in.Limit, defaultLimit, maxLimit)
	req := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupIdentifier: aws.String(in.Group), LogStreamNames: in.Streams,
		StartTime: aws.Int64(from.UnixMilli()), EndTime: aws.Int64(to.UnixMilli()), Limit: aws.Int32(eventsPage),
	}
	if in.Pattern != "" {
		req.FilterPattern = aws.String(in.Pattern)
	}
	var kept []types.FilteredLogEvent
	for {
		res, err := l.api.FilterLogEvents(ctx, req)
		switch {
		case ctx.Err() != nil:
			return out, errors.New("too many events to reach the newest in time: narrow the time range or add a pattern")
		case err != nil:
			return out, mcpserve.AWSError(err)
		}
		kept = append(kept, res.Events...)
		if len(kept) > limit {
			kept, out.Truncated = kept[len(kept)-limit:], true
		}
		if aws.ToString(res.NextToken) == "" {
			break
		}
		req.NextToken = res.NextToken
	}
	size := 0
	first := len(kept)
	for first > 0 {
		message, cutMessage := cut(aws.ToString(kept[first-1].Message))
		if size += len(message); size > maxResultBytes {
			out.Truncated = true
			break
		}
		out.Truncated = out.Truncated || cutMessage
		first--
		kept[first].Message = &message
	}
	for idx := range kept[first:] {
		e := &kept[first+idx]
		out.Events = append(out.Events, eventOutput{Time: stamp(e.Timestamp), Stream: aws.ToString(e.LogStreamName), Message: aws.ToString(e.Message)})
	}
	return out, nil
}

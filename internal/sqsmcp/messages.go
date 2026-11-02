package sqsmcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

const (
	maxPeek = 10
	// peekWaitSeconds long-polls briefly so a sparse queue still answers.
	peekWaitSeconds int32 = 2
	// peekVisibilitySeconds must stay above 0: the SDK omits a zero and the queue's own timeout (30 s or more) would hide the messages.
	peekVisibilitySeconds int32 = 1
	maxBodyBytes                = 32 << 10
	cacheSize                   = 50
	// minMaxReceiveCount is the lowest maxReceiveCount peek_messages accepts: below it a single read may send a message to the DLQ.
	minMaxReceiveCount = 3
	truncatedSuffix    = "…<truncated>"
)

// readMessage is a message as received, kept for repost_message.
type readMessage struct {
	Attributes    map[string]types.MessageAttributeValue
	QueueURL      string
	MessageID     string
	Body          string
	ReceiptHandle string
	GroupID       string
	TraceHeader   string
	AtLimit       bool
}

type peekInput struct {
	Queue string `json:"queue" jsonschema:"queue name or URL"`
	Max   int    `json:"max,omitempty" jsonschema:"number of messages to read, 1 to 10 (default 10)"`
}

type peekedMessage struct {
	Attributes      map[string]string `json:"attributes,omitempty"`
	DeliveriesLeft  *int              `json:"deliveries_left,omitempty"`
	MessageID       string            `json:"message_id"`
	Body            string            `json:"body"`
	SentAt          string            `json:"sent_at,omitempty"`
	FirstReceivedAt string            `json:"first_received_at,omitempty"`
	GroupID         string            `json:"group_id,omitempty"`
	ReceiveCount    int               `json:"receive_count"`
	Truncated       bool              `json:"truncated"`
	AtLimit         bool              `json:"at_limit"`
}

type peekOutput struct {
	Queue           string          `json:"queue"`
	Note            string          `json:"note"`
	Messages        []peekedMessage `json:"messages"`
	MaxReceiveCount int             `json:"max_receive_count,omitempty"`
}

const (
	peekHead = "Each message read counted as a receive. "
	peekTail = "On a standard queue this is a sample, not the whole queue, and a busy queue may return nothing. Do not read at_limit messages again: " +
		"repost_message can reset them once the user agrees."
	peekDLQ = "A message already at its maximum was moved to the dead-letter queue by this read and is not listed. "
)

// cut keeps at most maxBodyBytes of body, on a rune boundary, and marks the cut.
func cut(body string) (string, bool) {
	if len(body) <= maxBodyBytes {
		return body, false
	}
	n := maxBodyBytes
	for n > 0 && !utf8.RuneStart(body[n]) {
		n--
	}
	return body[:n] + truncatedSuffix, true
}

func attributeText(v types.MessageAttributeValue) string { //nolint:gocritic // signature fixed by the design; not a hot path.
	if v.StringValue != nil {
		return *v.StringValue
	}
	return fmt.Sprintf("<binary %d bytes>", len(v.BinaryValue))
}

// millis formats an SQS millisecond timestamp as RFC 3339 UTC; "" when missing.
func millis(s string) string {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// Peek reads messages without deleting them: they come back after a second, with their receive count raised.
func (q *queues) Peek(ctx context.Context, in *peekInput) (peekOutput, error) {
	u, err := q.url(ctx, in.Queue)
	if err != nil {
		return peekOutput{}, err
	}
	attrs, err := q.api.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(u), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameRedrivePolicy},
	})
	if err != nil {
		return peekOutput{}, mcpserve.AWSError(err)
	}
	policy, err := parseRedrive(attrs.Attributes[string(types.QueueAttributeNameRedrivePolicy)])
	if err != nil {
		return peekOutput{}, err
	}
	name := nameOf(u)
	limit := 0
	if policy != nil {
		limit = policy.MaxReceiveCount
		if limit < minMaxReceiveCount {
			return peekOutput{}, fmt.Errorf("queue %s has maxReceiveCount %d: reading it could send a message to its dead-letter queue (needs at least %d)", name, limit, minMaxReceiveCount)
		}
	}
	n := in.Max
	if n < 1 || n > maxPeek {
		n = maxPeek
	}
	got, err := q.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(u), MaxNumberOfMessages: int32(n), VisibilityTimeout: peekVisibilitySeconds,
		WaitTimeSeconds: peekWaitSeconds, MessageAttributeNames: []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
	})
	if err != nil {
		return peekOutput{}, mcpserve.AWSError(err)
	}
	note := peekHead + peekTail
	if policy != nil {
		note = peekHead + peekDLQ + peekTail
	}
	out := peekOutput{Queue: name, MaxReceiveCount: limit, Note: note, Messages: make([]peekedMessage, 0, len(got.Messages))}
	for i := range got.Messages {
		m := &got.Messages[i]
		count, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]) //nolint:errcheck // 0 stands for missing.
		body, truncated := cut(aws.ToString(m.Body))
		p := peekedMessage{
			MessageID: aws.ToString(m.MessageId), Body: body, Truncated: truncated, ReceiveCount: count,
			SentAt:          millis(m.Attributes[string(types.MessageSystemAttributeNameSentTimestamp)]),
			FirstReceivedAt: millis(m.Attributes[string(types.MessageSystemAttributeNameApproximateFirstReceiveTimestamp)]),
			GroupID:         m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
		}
		if len(m.MessageAttributes) > 0 {
			p.Attributes = make(map[string]string, len(m.MessageAttributes))
			for k, v := range m.MessageAttributes {
				text, cutText := cut(attributeText(v))
				p.Attributes[k], p.Truncated = text, p.Truncated || cutText
			}
		}
		if policy != nil {
			left := limit - count
			p.DeliveriesLeft, p.AtLimit = &left, left <= 1
		}
		out.Messages = append(out.Messages, p)
		q.cache.put(&readMessage{
			QueueURL: u, MessageID: p.MessageID, Body: aws.ToString(m.Body), ReceiptHandle: aws.ToString(m.ReceiptHandle),
			GroupID: p.GroupID, TraceHeader: m.Attributes[string(types.MessageSystemAttributeNameAWSTraceHeader)],
			Attributes: m.MessageAttributes, AtLimit: p.AtLimit,
		})
	}
	return out, nil
}

type repostInput struct {
	Queue     string `json:"queue" jsonschema:"queue name or URL"`
	MessageID string `json:"message_id" jsonschema:"id of an at_limit message returned by peek_messages"`
}

type repostOutput struct {
	MessageID      string `json:"message_id"`
	DeleteError    string `json:"delete_error,omitempty"`
	Note           string `json:"note"`
	DeleteAccepted bool   `json:"delete_accepted"`
}

const repostNote = "A copy was sent to the queue. Deleting the original is best effort: its receipt handle may have expired, and if anything received the " +
	"original since the read it may still exist, in this queue or in its dead-letter queue. Consumers may see a duplicate."

// Repost sends a fresh copy of a message read by peek_messages that is at its receive limit, then tries to delete the original.
func (q *queues) Repost(ctx context.Context, in *repostInput) (repostOutput, error) {
	u, err := q.url(ctx, in.Queue)
	if err != nil {
		return repostOutput{}, err
	}
	// take, not get: concurrent calls must not both send a copy.
	m, ok := q.cache.take(u, in.MessageID)
	if !ok {
		return repostOutput{}, fmt.Errorf("message %s was not read by this session, or is no longer remembered: peek_messages it again", in.MessageID)
	}
	if !m.AtLimit {
		q.cache.put(m)
		return repostOutput{}, fmt.Errorf("message %s is not at its receive limit: only at_limit messages can be reposted", in.MessageID)
	}
	send := &sqs.SendMessageInput{QueueUrl: aws.String(u), MessageBody: aws.String(m.Body), MessageAttributes: m.Attributes}
	if m.TraceHeader != "" {
		send.MessageSystemAttributes = map[string]types.MessageSystemAttributeValue{
			string(types.MessageSystemAttributeNameAWSTraceHeader): {DataType: aws.String("String"), StringValue: aws.String(m.TraceHeader)},
		}
	}
	if m.GroupID != "" {
		send.MessageGroupId = aws.String(m.GroupID)
	}
	if strings.HasSuffix(u, ".fifo") {
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			q.cache.put(m)
			return repostOutput{}, fmt.Errorf("generate deduplication id: %w", err)
		}
		send.MessageDeduplicationId = aws.String(hex.EncodeToString(id))
	}
	// One attempt only: a retry after a lost response would send a second copy.
	sent, err := q.api.SendMessage(ctx, send, func(o *sqs.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		q.cache.put(m)
		return repostOutput{}, fmt.Errorf("the copy may have been sent: check the queue before trying again (%w)", mcpserve.AWSError(err))
	}
	// The copy exists and the message stays forgotten (taken above), whatever the delete says.
	out := repostOutput{MessageID: aws.ToString(sent.MessageId), Note: repostNote}
	if _, err := q.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(u), ReceiptHandle: aws.String(m.ReceiptHandle)}); err != nil {
		out.DeleteError = mcpserve.AWSError(err).Error()
		return out, nil
	}
	out.DeleteAccepted = true
	return out, nil
}

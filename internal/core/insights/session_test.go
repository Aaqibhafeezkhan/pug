package insights

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/pug-sh/pug/internal/gen/proto/common/v1"
	insightsv1 "github.com/pug-sh/pug/internal/gen/proto/shared/insights/v1"
)

func sessionReq(metric insightsv1.SessionMetric, kind string, breakdown string) *insightsv1.QueryRequest {
	spec := &insightsv1.InsightQuerySpec{
		InsightType: insightsv1.InsightType_INSIGHT_TYPE_TRENDS.Enum(),
		Session: &insightsv1.SessionQuery{
			Metric: metric.Enum(),
			Scope:  &commonv1.EventFilter{Kind: proto.String(kind)},
		},
	}
	if breakdown != "" {
		spec.Breakdowns = []*insightsv1.Breakdown{{Property: proto.String(breakdown)}}
	}
	return &insightsv1.QueryRequest{
		Spec: spec,
		TimeRange: &commonv1.TimeRange{
			From: timestamppb.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
			To:   timestamppb.New(time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)),
		},
		Granularity: insightsv1.Granularity_GRANULARITY_DAY.Enum(),
	}
}

func TestBuildSessionTrendsQuery_EntryPageRaw(t *testing.T) {
	req := sessionReq(insightsv1.SessionMetric_SESSION_METRIC_ENTRY, "page_view", "$url")
	q, err := BuildSessionTrendsQuery(req, "proj_123")
	if err != nil {
		t.Fatalf("BuildSessionTrendsQuery: %v", err)
	}
	for _, want := range []string{
		"FROM events",
		"argMin(coalesce(url, ''), occur_time) AS breakdown_0",
		"toFloat64(count()) AS value",
		"'page_view' AS event_kind",
		"GROUP BY session_id",
	} {
		if !strings.Contains(q.SQL(), want) {
			t.Errorf("expected SQL to contain %q\nSQL:\n%s", want, q.SQL())
		}
	}
	if got := q.Properties(); len(got) != 1 || got[0] != "$url" {
		t.Errorf("properties = %v, want [$url]", got)
	}
}

func TestBuildSessionSegmentationQuery_AvgDurationRaw(t *testing.T) {
	req := sessionReq(insightsv1.SessionMetric_SESSION_METRIC_AVG_DURATION, "", "")
	req.Spec.InsightType = insightsv1.InsightType_INSIGHT_TYPE_SEGMENTATION.Enum()
	q, err := BuildSessionSegmentationQuery(req, "proj_123")
	if err != nil {
		t.Fatalf("BuildSessionSegmentationQuery: %v", err)
	}
	for _, want := range []string{
		"FROM events",
		"avg(dateDiff('second', start_time, end_time))",
		"'$session'",
	} {
		if want == "'$session'" {
			if strings.Contains(q.SQL(), want) {
				t.Errorf("segmentation SQL should not project an event_kind\nSQL:\n%s", q.SQL())
			}
			continue
		}
		if !strings.Contains(q.SQL(), want) {
			t.Errorf("expected SQL to contain %q\nSQL:\n%s", want, q.SQL())
		}
	}
}

func TestSessionExecution_ReadsEvents(t *testing.T) {
	req := sessionReq(insightsv1.SessionMetric_SESSION_METRIC_ENTRY, "page_view", "$url")
	trends, usedRollup, err := trendsQueryForExecution(req, "proj_123", time.Now())
	if err != nil {
		t.Fatalf("trendsQueryForExecution: %v", err)
	}
	if usedRollup || !strings.Contains(trends.SQL(), "FROM events") {
		t.Errorf("session trends must read events, usedRollup=%v\nSQL:\n%s", usedRollup, trends.SQL())
	}

	req.Spec.InsightType = insightsv1.InsightType_INSIGHT_TYPE_SEGMENTATION.Enum()
	req.Spec.Session.Metric = insightsv1.SessionMetric_SESSION_METRIC_SESSIONS.Enum()
	req.Spec.Breakdowns = nil
	seg, usedRollup, err := segmentationQueryForExecution(req, "proj_123", time.Now())
	if err != nil {
		t.Fatalf("segmentationQueryForExecution: %v", err)
	}
	if usedRollup || !strings.Contains(seg.SQL(), "FROM events") {
		t.Errorf("session segmentation must read events, usedRollup=%v\nSQL:\n%s", usedRollup, seg.SQL())
	}
}

func TestSessionTrendsExecution_BucketsInRequestedZone(t *testing.T) {
	req := sessionReq(insightsv1.SessionMetric_SESSION_METRIC_ENTRY, "page_view", "$url")
	req.Timezone = proto.String("Asia/Kolkata")
	q, _, err := trendsQueryForExecution(req, "proj_123", time.Now())
	if err != nil {
		t.Fatalf("trendsQueryForExecution: %v", err)
	}
	if !strings.Contains(q.SQL(), "toTimeZone(start_time, 'Asia/Kolkata')") {
		t.Errorf("session trends must bucket start_time in the requested zone\nSQL:\n%s", q.SQL())
	}
}

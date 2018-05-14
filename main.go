package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	monitoring "google.golang.org/api/monitoring/v3"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/common/log"
	"github.com/prometheus/prometheus/prompb"
)

type config struct {
	listenAddr string
	projectID  string
}

func runQuery(monitoringService *monitoring.Service, projectID string, q *prompb.Query) []*prompb.TimeSeries {
	result := []*prompb.TimeSeries{}

	filters := make([]string, 0)
	for _, m := range q.Matchers {
		var matcher string
		switch m.Type {
		case prompb.LabelMatcher_EQ:
			matcher = "="
		case prompb.LabelMatcher_NEQ:
			matcher = "!="
		case prompb.LabelMatcher_RE:
			fallthrough
		case prompb.LabelMatcher_NRE:
			log.Error("unsupported matcher")
			return result
		}

		switch m.Name {
		case "__name__":
			filters = append(filters, fmt.Sprintf("%s%s\"%s\"", "metric.type", matcher, m.Value))
		default:
			filters = append(filters, fmt.Sprintf("%s%s\"%s\"", m.Name, matcher, m.Value))
		}
	}

	startTime := time.Unix(q.StartTimestampMs/1000, 0)
	endTime := time.Unix(q.EndTimestampMs/1000, 0)
	timeSeriesListCall := monitoringService.Projects.TimeSeries.List(projectResource(projectID)).
		Filter(strings.Join(filters, " AND ")).
		IntervalStartTime(startTime.Format(time.RFC3339Nano)).
		IntervalEndTime(endTime.Format(time.RFC3339Nano))

	stackdriverTimeSeries := make([]*monitoring.TimeSeries, 0)
	for {
		page, err := timeSeriesListCall.Do()
		if err != nil {
			log.Errorf("%+v", err)
			return result
		}
		if page == nil {
			break
		}
		stackdriverTimeSeries = append(stackdriverTimeSeries, page.TimeSeries...)
		if page.NextPageToken == "" {
			break
		}
	}

	for _, sts := range stackdriverTimeSeries {
		ts := &prompb.TimeSeries{}
		for key, value := range sts.Metric.Labels {
			ts.Labels = append(ts.Labels, &prompb.Label{Name: key, Value: value})
		}
		for key, value := range sts.Resource.Labels {
			ts.Labels = append(ts.Labels, &prompb.Label{Name: key, Value: value})
		}
		for _, point := range sts.Points {
			if sts.ValueType != "DISTRIBUTION" {
				var value float64
				switch sts.ValueType {
				case "BOOL":
					if *point.Value.BoolValue {
						value = 1
					} else {
						value = 0
					}
				case "INT64":
					value = float64(*point.Value.Int64Value)
				case "DOUBLE":
					value = *point.Value.DoubleValue
				}
				timestamp, err := time.Parse(time.RFC3339Nano, point.Interval.EndTime)
				if err != nil {
					log.Errorf("%+v", err)
					return result
				}
				ts.Samples = append(ts.Samples, &prompb.Sample{Value: value, Timestamp: timestamp.Unix() * 1000})
			} else {
				log.Error("unsupported type")
				return result
			}
		}
		result = append(result, ts)
	}

	log.Infof("Returned %d time series.", len(result))

	return result
}

func createMonitoringService() (*monitoring.Service, error) {
	ctx := context.Background()

	googleClient, err := google.DefaultClient(ctx, monitoring.MonitoringReadScope)
	if err != nil {
		return nil, fmt.Errorf("Error creating Google client: %+v", err)
	}

	monitoringService, err := monitoring.New(googleClient)
	if err != nil {
		return nil, fmt.Errorf("Error creating Google Stackdriver Monitoring service: %+v", err)
	}

	return monitoringService, nil
}

func projectResource(projectID string) string {
	return "projects/" + projectID
}

func main() {
	var cfg config
	flag.StringVar(&cfg.listenAddr, "web.listen-address", ":9201", "Address to listen on for web endpoints.")
	flag.StringVar(&cfg.projectID, "project-id", "", "project ID.")
	flag.Parse()

	var err error
	monitoringService, err := createMonitoringService()
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}

	http.HandleFunc("/read", func(w http.ResponseWriter, r *http.Request) {
		compressed, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req prompb.ReadRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if len(req.Queries) != 1 {
			http.Error(w, "Can only handle one query.", http.StatusBadRequest)
			return
		}

		resp := prompb.ReadResponse{
			Results: []*prompb.QueryResult{
				{Timeseries: runQuery(monitoringService, cfg.projectID, req.Queries[0])},
			},
		}
		data, err := proto.Marshal(&resp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/x-protobuf")
		if _, err := w.Write(snappy.Encode(nil, data)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	log.Infof("Listening on %s", cfg.listenAddr)
	http.ListenAndServe(cfg.listenAddr, nil)
}
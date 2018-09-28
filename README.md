# stackdriver_read_adapter

Prometheus read adapter for Stackdriver Monitoring.

## Setup

Launch read adapter.

```
$ stackdriver_read_adapter --project-id <GCP project id>
```

And add remote_read setting to Prometheus.

```
remote_read:
  - url: http://localhost:9201/read
```

## Usage
### Basic label match
PromQL
```
{__name__="kubernetes.io/container/cpu/limit_utilization",resource_labels_namespace_name="default"}
```

is converted to following Stackdriver filter.
```
metric.type = "kubernetes.io/container/cpu/limit_utilization" AND resource.labels.namespace_name = "default"
```

`__name__` label doesn't allow to use `.` character, need to use label matcher for `__name__` matching.

### Regex Match
Supported pattern

| Prometheus label matcher | Stackdriver filter |
---------------------------|---------------------
| label=~"foo&#124;bar&#124;baz" | one_of("foo", "bar", "baz") |
| label=~"foo.*" | starts_with("foo") |
| label=~".*foo" | ends_with("foo") |
| label=~"foo" | has_substring("foo") |

## Notice
Distribution Metrics is not supported.

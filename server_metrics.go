package grpc_prometheus

import (
	prom "github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

// ServerMetrics represents a collection of metrics to be registered on a
// Prometheus metrics registry for a gRPC server.
type ServerMetrics struct {
	serverStartedCounter          *prom.CounterVec
	serverHandledCounter          *prom.CounterVec
	serverStreamMsgReceived       *prom.CounterVec
	serverStreamMsgSent           *prom.CounterVec
	serverHandledHistogramEnabled bool
	serverHandledHistogramOpts    prom.HistogramOpts
	serverHandledHistogram        *prom.HistogramVec
}

// NewServerMetrics returns a ServerMetrics object. Use a new instance of
// ServerMetrics when not using the default Prometheus metrics registry, for
// example when wanting to control which metrics are added to a registry as
// opposed to automatically adding metrics via init functions.
func NewServerMetrics() *ServerMetrics {
	return &ServerMetrics{
		serverStartedCounter: prom.NewCounterVec(
			prom.CounterOpts{
				Name: "grpc_server_started_total",
				Help: "Total number of RPCs started on the server.",
			}, []string{"grpc_type", "grpc_service", "grpc_method"}),
		serverHandledCounter: prom.NewCounterVec(
			prom.CounterOpts{
				Name: "grpc_server_handled_total",
				Help: "Total number of RPCs completed on the server, regardless of success or failure.",
			}, []string{"grpc_type", "grpc_service", "grpc_method", "grpc_code"}),
		serverStreamMsgReceived: prom.NewCounterVec(
			prom.CounterOpts{
				Name: "grpc_server_msg_received_total",
				Help: "Total number of RPC stream messages received on the server.",
			}, []string{"grpc_type", "grpc_service", "grpc_method"}),
		serverStreamMsgSent: prom.NewCounterVec(
			prom.CounterOpts{
				Name: "grpc_server_msg_sent_total",
				Help: "Total number of gRPC stream messages sent by the server.",
			}, []string{"grpc_type", "grpc_service", "grpc_method"}),
		serverHandledHistogramEnabled: false,
		serverHandledHistogramOpts: prom.HistogramOpts{
			Name:    "grpc_server_handling_seconds",
			Help:    "Histogram of response latency (seconds) of gRPC that had been application-level handled by the server.",
			Buckets: prom.DefBuckets,
		},
		serverHandledHistogram: nil,
	}
}

type HistogramOption func(*prom.HistogramOpts)

// WithHistogramBuckets allows you to specify custom bucket ranges for histograms if EnableHandlingTimeHistogram is on.
func WithHistogramBuckets(buckets []float64) HistogramOption {
	return func(o *prom.HistogramOpts) { o.Buckets = buckets }
}

// EnableHandlingTimeHistogram enables histograms being registered when
// registering the ServerMetrics on a Prometheus registry. Histograms can be
// expensive on Prometheus servers. It takes options to configure histogram
// options such as the defined buckets.
func (m *ServerMetrics) EnableHandlingTimeHistogram(opts ...HistogramOption) {
	for _, o := range opts {
		o(&m.serverHandledHistogramOpts)
	}
	if !m.serverHandledHistogramEnabled {
		m.serverHandledHistogram = prom.NewHistogramVec(
			m.serverHandledHistogramOpts,
			[]string{"grpc_type", "grpc_service", "grpc_method"},
		)
	}
	m.serverHandledHistogramEnabled = true
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once
// the last descriptor has been sent.
func (m *ServerMetrics) Describe(ch chan<- *prom.Desc) {
	m.serverStartedCounter.Describe(ch)
	m.serverHandledCounter.Describe(ch)
	m.serverStreamMsgReceived.Describe(ch)
	m.serverStreamMsgSent.Describe(ch)
	if m.serverHandledHistogramEnabled {
		m.serverHandledHistogram.Describe(ch)
	}
}

// Collect is called by the Prometheus registry when collecting
// metrics. The implementation sends each collected metric via the
// provided channel and returns once the last metric has been sent.
func (m *ServerMetrics) Collect(ch chan<- prom.Metric) {
	m.serverStartedCounter.Collect(ch)
	m.serverHandledCounter.Collect(ch)
	m.serverStreamMsgReceived.Collect(ch)
	m.serverStreamMsgSent.Collect(ch)
	if m.serverHandledHistogramEnabled {
		m.serverHandledHistogram.Collect(ch)
	}
}

// UnaryServerInterceptor is a gRPC server-side interceptor that provides Prometheus monitoring for Unary RPCs.
func (m *ServerMetrics) UnaryServerInterceptor() func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		monitor := newServerReporter(m, Unary, info.FullMethod)
		monitor.ReceivedMessage()
		resp, err := handler(ctx, req)
		monitor.Handled(grpc.Code(err))
		if err == nil {
			monitor.SentMessage()
		}
		return resp, err
	}
}

// StreamServerInterceptor is a gRPC server-side interceptor that provides Prometheus monitoring for Streaming RPCs.
func (m *ServerMetrics) StreamServerInterceptor() func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		monitor := newServerReporter(m, streamRpcType(info), info.FullMethod)
		err := handler(srv, &monitoredServerStream{ss, monitor})
		monitor.Handled(grpc.Code(err))
		return err
	}
}

// InitializeMetrics initializes all metrics, with their appropriate null
// value, for all gRPC methods registered on a gRPC server (or any other
// struct that meets the prometheusServer interface). This is useful, to ensure
// that all metrics exist when collecting and querying.
func (m *ServerMetrics) InitializeMetrics(server prometheusServer) {
	serviceInfo := server.GetServiceInfo()
	for serviceName, info := range serviceInfo {
		for _, mInfo := range info.Methods {
			preRegisterMethod(m, serviceName, &mInfo)
		}
	}
}

// Register registers all server metrics in a given metrics registry. Depending
// on histogram options and whether they are enabled, histogram metrics are
// also registered.
//
// Deprecated: ServerMetrics implements Prometheus Collector interface. You can
// register an instance of ServerMetrics directly by using
// prometheus.Register(m).
func (m *ServerMetrics) Register(r prom.Registerer) error {
	return r.Register(m)
}

// MustRegister tries to register all server metrics and panics on an error.
//
// Deprecated: ServerMetrics implements Prometheus Collector interface. You can
// register an instance of ServerMetrics directly by using
// prometheus.MustRegister(m).
func (m *ServerMetrics) MustRegister(r prom.Registerer) {
	r.MustRegister(m)
}

func streamRpcType(info *grpc.StreamServerInfo) grpcType {
	if info.IsClientStream && !info.IsServerStream {
		return ClientStream
	} else if !info.IsClientStream && info.IsServerStream {
		return ServerStream
	}
	return BidiStream
}

// monitoredStream wraps grpc.ServerStream allowing each Sent/Recv of message to increment counters.
type monitoredServerStream struct {
	grpc.ServerStream
	monitor *serverReporter
}

func (s *monitoredServerStream) SendMsg(m interface{}) error {
	err := s.ServerStream.SendMsg(m)
	if err == nil {
		s.monitor.SentMessage()
	}
	return err
}

func (s *monitoredServerStream) RecvMsg(m interface{}) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.monitor.ReceivedMessage()
	}
	return err
}

// preRegisterMethod is invoked on Register of a Server, allowing all gRPC services labels to be pre-populated.
func preRegisterMethod(metrics *ServerMetrics, serviceName string, mInfo *grpc.MethodInfo) {
	methodName := mInfo.Name
	methodType := string(typeFromMethodInfo(mInfo))
	// These are just references (no increments), as just referencing will create the labels but not set values.
	metrics.serverStartedCounter.GetMetricWithLabelValues(methodType, serviceName, methodName)
	metrics.serverStreamMsgReceived.GetMetricWithLabelValues(methodType, serviceName, methodName)
	metrics.serverStreamMsgSent.GetMetricWithLabelValues(methodType, serviceName, methodName)
	if metrics.serverHandledHistogramEnabled {
		metrics.serverHandledHistogram.GetMetricWithLabelValues(methodType, serviceName, methodName)
	}
	for _, code := range allCodes {
		metrics.serverHandledCounter.GetMetricWithLabelValues(methodType, serviceName, methodName, code.String())
	}
}

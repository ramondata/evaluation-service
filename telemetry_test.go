package main

import (
 "context"
 "net/http"
 "net/http/httptest"
 "os"
 "testing"

 "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
 "go.opentelemetry.io/otel"
 "go.opentelemetry.io/otel/propagation"
 sdktrace "go.opentelemetry.io/otel/sdk/trace"
 "go.opentelemetry.io/otel/trace"
)

func TestHTTPPropagatesTraceAcrossServiceBoundary(t *testing.T) {
 provider:=sdktrace.NewTracerProvider()
 defer func(){_ = provider.Shutdown(context.Background())}()
 old:=otel.GetTracerProvider();otel.SetTracerProvider(provider);defer otel.SetTracerProvider(old)
 otel.SetTextMapPropagator(propagation.TraceContext{})
 c,parent:=provider.Tracer("test").Start(context.Background(),"evaluation")
 defer parent.End()
 var downstream trace.TraceID
 server:=httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
 downstream=trace.SpanContextFromContext(r.Context()).TraceID();w.WriteHeader(204)
 }),"flag"));defer server.Close()
 request,err:=http.NewRequestWithContext(c,http.MethodGet,server.URL,nil)
 if err!=nil {t.Fatal(err)}
 client:=http.Client{Transport:otelhttp.NewTransport(http.DefaultTransport)}
 response,err:=client.Do(request);if err!=nil {t.Fatal(err)}
 defer func(){_ = response.Body.Close()}()
 if downstream!=parent.SpanContext().TraceID(){t.Fatal("downstream lost trace context")}
}

func TestDemoFailureRequiresTokenAndCanBeReversed(t *testing.T) {
 t.Setenv("DEMO_ENABLED","true");t.Setenv("DEMO_TOKEN","test-only")
 defer demoFailure.Store(false)
 for _,test:=range []struct{method,token string;want int;active bool}{
 {"POST","wrong",401,false},{"POST","test-only",204,true},{"DELETE","test-only",204,false},
 } {
 r:=httptest.NewRequest(test.method,"/demo/failure",nil);r.Header.Set("Authorization","Bearer "+test.token)
 w:=httptest.NewRecorder();demoFailureHandler(w,r)
 if w.Code!=test.want||demoFailure.Load()!=test.active{t.Fatalf("unexpected failure control state: %d",w.Code)}
 }
 os.Unsetenv("DEMO_ENABLED")
}

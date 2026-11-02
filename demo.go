package main

import (
 "crypto/subtle"
 "net/http"
 "os"
 "strings"
 "sync/atomic"
)

// Apenas a rota de negocio falha. Probes seguem saudaveis para demonstrar o webhook.
var demoFailure atomic.Bool
func demoFailureHandler(w http.ResponseWriter,r *http.Request) {
 token:=os.Getenv("DEMO_TOKEN")
 supplied:=strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer ")
 if os.Getenv("DEMO_ENABLED")!="true" || token=="" { http.NotFound(w,r);return }
 if subtle.ConstantTimeCompare([]byte(token),[]byte(supplied))!=1 { http.Error(w,"unauthorized",401);return }
 switch r.Method {
 case http.MethodPost: demoFailure.Store(true)
 case http.MethodDelete: demoFailure.Store(false)
 default: w.WriteHeader(http.StatusMethodNotAllowed);return
 }
 w.WriteHeader(http.StatusNoContent)
}

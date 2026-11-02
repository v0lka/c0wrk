// SPDX-License-Identifier: Apache-2.0
//
// dispatch.go — reflection RPC over the injected dispatch target (at
// runtime a *backend.FrontendAPI): every EXPORTED method is callable with
// positional JSON args, exactly like the generated wailsjs bindings call
// them. The denylist is the only reduction — entries exist solely for
// methods that are hazards of the WEB TRANSPORT itself, each with its
// reason inline; never an entry for ordinary business policy.
// specs/domains/web-access.md (Invariants — denylist).

package webaccess

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"runtime/debug"
)

// deniedMethods names the exported methods that are NOT reachable over the
// web transport. Each entry carries its web-specific reason.
var deniedMethods = map[string]string{
	// EmitSessionEvent injects a synthetic event into the app's trusted
	// event bus — the stream the desktop UI (and this sidecar's own /ws
	// fan-out) replays into chat/plan/HITL state. A web client must be a
	// CONSUMER of events, never a producer: letting a tab forge them is a
	// spoofing primitive against the user's own desktop session.
	"EmitSessionEvent": "web clients consume events over /ws; injecting synthetic session events into the desktop event bus is not exposed",

	// PasteFromClipboard returns the host system clipboard to the caller.
	// Browsers gate clipboard reads behind user gestures and permissions;
	// exposing this RPC would hand the host clipboard to whatever the tab
	// is running — an OS-wide exfiltration channel. Mirrored UIs paste via
	// the browser's own clipboard access instead.
	"PasteFromClipboard": "host clipboard content is not exposed to the browser (OS-level exfiltration surface)",
}

// DefaultDenylist returns a copy of the web-transport denylist (method →
// reason); exported for the surface contract test and diagnostics.
func DefaultDenylist() map[string]string {
	out := make(map[string]string, len(deniedMethods))
	for k, v := range deniedMethods {
		out[k] = v
	}
	return out
}

// methodNameRe admits only Go-identifier-shaped method names; anything else
// is a 404 before reflection ever runs.
var methodNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// errType is the reflection handle for the error interface.
var errType = reflect.TypeOf((*error)(nil)).Elem()

// resolveMethod finds method on the primary dispatch target, falling back to
// the extra surface (the minimal App-level adapter — see
// Options.ExtraDispatch). The denylist is applied by the callers BEFORE
// resolution, so it governs the union. Returns the zero Value when neither
// target exposes the method.
func (s *Server) resolveMethod(method string) reflect.Value {
	if m := reflect.ValueOf(s.dispatch).MethodByName(method); m.IsValid() {
		return m
	}
	if v := reflect.ValueOf(s.extraDispatch); v.IsValid() {
		if v.Kind() != reflect.Pointer || !v.IsNil() {
			if m := v.MethodByName(method); m.IsValid() {
				return m
			}
		}
	}
	return reflect.Value{}
}

// errBodyTooLarge marks an oversize request body (→ HTTP 413).
var errBodyTooLarge = errors.New("request body too large")

// uploadBodyMethods carries the per-method body-limit overrides: RPCs whose
// positional arguments include a bulk upload payload. Keys must name
// exported methods of the dispatch target (the surface contract test pins
// the correspondence); absent methods keep maxBodyBytes. Transport sizing
// for an existing backend capability, not a surface change — the raised
// limit matches the backend's own attachment size cap.
var uploadBodyMethods = map[string]int64{
	// AttachFileContent carries a whole attachment as a base64 string.
	"AttachFileContent": maxUploadBodyBytes,
}

// bodyLimitFor returns the request-body limit for the given RPC method.
func bodyLimitFor(method string) int64 {
	if limit, ok := uploadBodyMethods[method]; ok {
		return limit
	}
	return maxBodyBytes
}

// readAllLimited reads the request body under the given limit via
// http.MaxBytesReader; an oversize body is reported as errBodyTooLarge.
func readAllLimited(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return body, nil
}

// handleRPC is POST /api/rpc/{method}: positional JSON args in, JSON result
// out, uniform error envelope on every failure. Wired behind requireAuth.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	if !methodNameRe.MatchString(method) {
		writeJSONError(w, http.StatusNotFound, "unknown method")
		return
	}
	if reason, denied := deniedMethods[method]; denied {
		writeJSONError(w, http.StatusForbidden, reason)
		return
	}
	m := s.resolveMethod(method)
	if !m.IsValid() {
		// MethodByName never resolves unexported methods, so an invalid
		// value means "not part of the surface".
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown method %q", method))
		return
	}

	body, ok := s.readJSONBody(w, r, bodyLimitFor(method))
	if !ok {
		return
	}
	rawArgs, err := decodeArgs(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "request body must be a JSON array of positional arguments")
		return
	}

	mt := m.Type()
	if mt.IsVariadic() {
		// No FrontendAPI method is variadic today; fail closed rather than
		// guess a spreading convention (the surface contract test fails
		// loudly if one appears).
		writeJSONError(w, http.StatusNotImplemented, "variadic methods are not supported over the web transport")
		return
	}
	if len(rawArgs) != mt.NumIn() {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("method %q expects %d argument(s), got %d", method, mt.NumIn(), len(rawArgs)))
		return
	}
	args := make([]reflect.Value, len(rawArgs))
	for i, raw := range rawArgs {
		ptr := reflect.New(mt.In(i))
		if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("argument %d of %q: %v", i+1, method, err))
			return
		}
		args[i] = ptr.Elem()
	}

	results, panicVal := s.callRecovered(m, args)
	if panicVal != nil {
		s.log.Error("webaccess: rpc panic", "method", method, "panic", panicVal, "stack", string(debug.Stack()))
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Return convention (mirrors Wails): () | (T) | (T, error) | (error).
	// A non-nil trailing error-typed result becomes the 500 envelope;
	// anything else is marshaled as the JSON result.
	if n := len(results); n > 0 && mt.Out(n-1).Implements(errType) {
		if errOut, _ := results[n-1].Interface().(error); errOut != nil {
			writeJSONError(w, http.StatusInternalServerError, errOut.Error())
			return
		}
		results = results[:n-1]
	}
	var payload any
	switch len(results) {
	case 0:
		payload = nil
	case 1:
		payload = results[0].Interface()
	default:
		vals := make([]any, len(results))
		for i, rv := range results {
			vals[i] = rv.Interface()
		}
		payload = vals
	}
	writeJSONValue(w, payload)
}

// callRecovered invokes m.Call under a recover so a panic inside the
// dispatched method becomes an error, never a crash — the goroutine serving
// the request survives long enough to report it.
func (s *Server) callRecovered(m reflect.Value, args []reflect.Value) (results []reflect.Value, panicVal any) {
	defer func() {
		if p := recover(); p != nil {
			panicVal = p
		}
	}()
	return m.Call(args), nil
}

// writeJSONValue marshals then writes, so a marshaling failure is a 500
// envelope instead of a half-written 200.
func writeJSONValue(w http.ResponseWriter, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode result")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}

// decodeArgs parses the request body as a positional argument array. An
// empty body and a JSON null both mean zero arguments (bare POST for a
// zero-arg method).
func decodeArgs(body []byte) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Dispatchable reports whether method is callable over the web transport:
// exported on the dispatch target (or the opted-in extra surface) and not
// denylisted. Exported for the surface contract test.
func (s *Server) Dispatchable(method string) bool {
	if _, denied := deniedMethods[method]; denied {
		return false
	}
	return s.resolveMethod(method).IsValid()
}

// recoverWrap turns any panic in the wrapped handler into a 500 envelope.
// It wraps every route (outermost) so injected middleware and the static
// handler are covered too; JSON handlers write only after all fallible
// work, so a pre-write panic lands cleanly in the envelope. Panics are
// logged with the stack, never with request bodies (secrets such as login
// tokens).
func (s *Server) recoverWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("webaccess: handler panic",
					"method", r.Method, "path", r.URL.Path,
					"panic", p, "stack", string(debug.Stack()))
				writeJSONError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

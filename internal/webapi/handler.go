// Package webapi adapts the existing Wails App methods to JSON over HTTP.
package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

var allowedMethods = map[string]bool{
	"CancelAllTranscode": true, "CancelPauseRemuxAfterCurrent": true,
	"CancelPauseTranscodeAfterCurrent": true, "CancelTranscodeTask": true,
	"ClearCompletedTranscodeTasks": true, "GetConfig": true,
	"GetErrorLogs": true, "GetRemuxPauseStatus": true, "GetRemuxProgress": true,
	"GetStats": true, "GetSystemInfo": true, "GetTasks": true,
	"GetTranscodeGlobalStatus": true, "GetTranscodeMaxWorkers": true,
	"GetTranscodePauseStatus": true, "GetTranscodeSettings": true,
	"GetTranscodeTasks": true, "IsAutoStartEnabled": true,
	"OpenTranscodeErrorLog": true, "PauseRemux": true,
	"PauseRemuxAfterCurrent": true, "PauseTranscode": true,
	"PauseTranscodeAfterCurrent": true, "RequestShutdown": true,
	"ResumeRemux": true, "ResumeTranscode": true, "SaveConfig": true,
	"SaveTranscodeSettings": true, "ScanMultiplePaths": true, "ScanNow": true,
	"ScanVideoFolder": true, "SelectFolder": true, "SetAutoStart": true,
	"SetTranscodeMaxWorkers": true, "ShutdownAfterCompletion": true,
	"ShutdownNow": true, "StartTranscode": true,
}

type Handler struct {
	target reflect.Value
}

func New(target interface{}) *Handler {
	return &Handler{target: reflect.ValueOf(target)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	methodName := strings.TrimPrefix(r.URL.Path, "/api/app/")
	if !allowedMethods[methodName] {
		writeError(w, http.StatusNotFound, "unknown app method")
		return
	}
	method := h.target.MethodByName(methodName)
	if !method.IsValid() {
		writeError(w, http.StatusNotFound, "app method is unavailable")
		return
	}

	var request struct {
		Args []json.RawMessage `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	methodType := method.Type()
	if len(request.Args) != methodType.NumIn() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("expected %d arguments", methodType.NumIn()))
		return
	}

	inputs := make([]reflect.Value, methodType.NumIn())
	for i := range inputs {
		value := reflect.New(methodType.In(i))
		if err := json.Unmarshal(request.Args[i], value.Interface()); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid argument %d: %v", i+1, err))
			return
		}
		inputs[i] = value.Elem()
	}

	outputs, callErr := call(method, inputs)
	if callErr != nil {
		writeError(w, http.StatusInternalServerError, callErr.Error())
		return
	}
	result, err := unpack(outputs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
}

func call(method reflect.Value, inputs []reflect.Value) (outputs []reflect.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("app method panic: %v", recovered)
		}
	}()
	return method.Call(inputs), nil
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

func unpack(outputs []reflect.Value) (interface{}, error) {
	if len(outputs) > 0 && outputs[len(outputs)-1].Type().Implements(errorType) {
		last := outputs[len(outputs)-1]
		outputs = outputs[:len(outputs)-1]
		if !last.IsNil() {
			return nil, last.Interface().(error)
		}
	}
	if len(outputs) == 0 {
		return nil, nil
	}
	if len(outputs) == 1 {
		return outputs[0].Interface(), nil
	}
	result := make([]interface{}, len(outputs))
	for i := range outputs {
		result[i] = outputs[i].Interface()
	}
	return result, nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

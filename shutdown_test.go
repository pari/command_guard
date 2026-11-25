package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownCoordinatorRejectsNewWorkAfterShutdown(t *testing.T) {
	sc := newShutdownCoordinator()

	if !sc.BeginWork() {
		t.Fatal("expected work to start before shutdown")
	}
	if !sc.BeginShutdown() {
		t.Fatal("expected first shutdown call to start shutdown")
	}
	if sc.BeginShutdown() {
		t.Fatal("expected second shutdown call to be rejected")
	}
	if sc.BeginWork() {
		t.Fatal("expected new work to be rejected after shutdown")
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if sc.waitForWork(timeoutCtx) {
		t.Fatal("expected active work to block shutdown wait")
	}
	cancel()

	sc.DoneWork()

	doneCtx, doneCancel := context.WithTimeout(context.Background(), time.Second)
	defer doneCancel()
	if !sc.waitForWork(doneCtx) {
		t.Fatal("expected shutdown wait to complete after active work finishes")
	}
}

func TestShutdownMiddlewareRejectsRequestsDuringShutdown(t *testing.T) {
	previousShutdown := appShutdown
	appShutdown = newShutdownCoordinator()
	t.Cleanup(func() {
		appShutdown = previousShutdown
	})

	appShutdown.BeginShutdown()

	called := false
	handler := shutdownMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	handler.ServeHTTP(recorder, request)

	if called {
		t.Fatal("expected wrapped handler not to be called during shutdown")
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, recorder.Code)
	}
}

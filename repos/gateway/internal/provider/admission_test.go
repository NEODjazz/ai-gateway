package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmissionRejectsWhenExecutionAndQueueAreFull(t *testing.T) {
	controller := newAdmissionController(1, 1, time.Second)
	releaseFirst, err := controller.acquire(context.Background(), "ollama")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { releaseFirst() }()

	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		release, acquireErr := controller.acquire(context.Background(), "ollama")
		if acquireErr != nil {
			errs <- acquireErr
			return
		}
		acquired <- release
	}()

	deadline := time.Now().Add(time.Second)
	for controller.queued.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if controller.queued.Load() != 1 {
		t.Fatal("request did not enter the bounded queue")
	}

	_, err = controller.acquire(context.Background(), "ollama")
	var admissionErr *AdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Provider != "ollama" || admissionErr.RetryAfter != time.Second {
		t.Fatalf("expected bounded queue rejection, got %#v", err)
	}

	releaseFirst()
	releaseFirst = func() {}
	select {
	case release := <-acquired:
		release()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("queued request was not admitted after release")
	}
}

func TestAdmissionQueueTimesOut(t *testing.T) {
	controller := newAdmissionController(1, 1, 20*time.Millisecond)
	release, err := controller.acquire(context.Background(), "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = controller.acquire(context.Background(), "provider-a")
	var admissionErr *AdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.RetryAfter != time.Second {
		t.Fatalf("expected admission timeout with rounded retry delay, got %v", err)
	}
}

func TestAdmissionHonorsContextCancellation(t *testing.T) {
	controller := newAdmissionController(1, 1, time.Second)
	release, err := controller.acquire(context.Background(), "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = controller.acquire(ctx, "provider-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

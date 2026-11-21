package bridge

import "testing"

func TestFeishuReconnectTelemetry(t *testing.T) {
	reconnects := 0
	connections := Connections{OnFeishuReconnect: func() { reconnects++ }}
	initial := &lifecycleChannel{}
	connections.trackReceiverLifecycle(&Lark{Channel: initial})

	initial.onReady()
	if reconnects != 0 || !connections.ready.Load() {
		t.Fatalf("initial Ready: reconnects = %d, ready = %t", reconnects, connections.ready.Load())
	}

	initial.onDisconnected()
	if reconnects != 0 || connections.ready.Load() {
		t.Fatalf("SDK disconnect: reconnects = %d, ready = %t", reconnects, connections.ready.Load())
	}

	for want := 1; want <= 2; want++ {
		initial.onReconnected()
		if reconnects != want || !connections.ready.Load() {
			t.Fatalf("SDK reconnect %d: reconnects = %d, ready = %t", want, reconnects, connections.ready.Load())
		}
	}

	replacement := &lifecycleChannel{}
	connections.trackReceiverLifecycle(&Lark{Channel: replacement})
	initial.onReconnected()
	if reconnects != 2 || connections.ready.Load() {
		t.Fatalf("stale SDK reconnect: reconnects = %d, ready = %t", reconnects, connections.ready.Load())
	}

	replacement.onReady()
	if reconnects != 2 || !connections.ready.Load() {
		t.Fatalf("replacement Ready: reconnects = %d, ready = %t", reconnects, connections.ready.Load())
	}

	replacement.onDisconnected()
	replacement.onReconnected()
	if reconnects != 3 || !connections.ready.Load() {
		t.Fatalf("replacement SDK reconnect: reconnects = %d, ready = %t", reconnects, connections.ready.Load())
	}
}

func TestFeishuReconnectTelemetryWithoutObserver(t *testing.T) {
	var connections Connections
	receiver := &lifecycleChannel{}
	connections.trackReceiverLifecycle(&Lark{Channel: receiver})

	receiver.onReconnected()
	if !connections.ready.Load() {
		t.Fatal("SDK reconnect did not restore readiness with a nil observer")
	}
}

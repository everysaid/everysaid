module go.mau.fi/mautrix-meta

go 1.26.0

require (
	github.com/apache/thrift v0.24.0
	github.com/beeper/poly1305 v0.0.0-20250815183548-d4eede7bbf3c
	github.com/coder/websocket v1.8.15
	github.com/google/go-querystring v1.2.0
	github.com/google/uuid v1.6.0
	github.com/imroc/req/v3 v3.56.0
	github.com/refraction-networking/utls v1.8.2
	github.com/rs/zerolog v1.35.1
	github.com/tidwall/gjson v1.19.0
	go.mau.fi/libsignal v0.2.2
	go.mau.fi/util v0.10.1
	go.mau.fi/whatsmeow v0.0.0-20260929112325-8b41cfe6d9c4
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.59.0
	google.golang.org/protobuf v1.36.12
)

replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20260808092153-100cef0a2fbd

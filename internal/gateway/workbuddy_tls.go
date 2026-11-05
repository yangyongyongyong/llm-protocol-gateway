package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Reproducing the desktop client's TLS ClientHello.
//
// Request headers can match byte for byte and still leave the connection
// telling: the TLS handshake finished long before HTTP starts. Go's crypto/tls
// has its own fixed shape — its own cipher list, the post-quantum
// X25519MLKEM768 group, SCT (18) and status_request (5) extensions — which is
// nothing like the official client's Node 22 / OpenSSL 3.x stack. A risk engine
// that looks at JA3/JA4 before headers sees the difference immediately.
//
// Measured on real captures (misakano7545/workbuddy2api-panel,
// internal/upstream/tlsfingerprint.go — full ClientHello recorded through a MITM
// on 2026-11-01):
//
//	www.workbuddy.cn  JA3 71dc8c533dd919ae9f4963224a4ba8fd
//	www.workbuddy.ai  JA3 71dc8c533dd919ae9f4963224a4ba8fd
//	                  JA4 t13d0a1200_5d04281c6031_b76c5ee2507d
//
// Both realms are identical, so one spec covers CN and intl — no per-realm
// profile. Every outbound path (chat, report, /v3/config, billing) uses it: 420
// handshakes matched byte for byte.
//
// One premise matters: the official chat runs in the **Node main process**, not
// the renderer's Chromium, so its ClientHello carries **no ALPN**. That is
// convenient here — this gateway already refuses h2 to avoid half-dead
// connections, so "look like the client" and "no h2" point the same way.
//
// The renderer has a separate Chromium fingerprint (16 ciphers, 18 extensions,
// ALPN=h2); nothing in this gateway sends renderer-shaped requests, so it is not
// reproduced.

const (
	// workBuddyTLSFingerprintNode reproduces the official Node/OpenSSL shape.
	workBuddyTLSFingerprintNode = "node"
	// workBuddyTLSFingerprintGo uses crypto/tls native shape (rollback/diagnosis).
	workBuddyTLSFingerprintGo = "go"

	// workBuddyTLSFingerprintEnv selects the profile: node (default) or go.
	workBuddyTLSFingerprintEnv = "WORKBUDDY_TLS_FINGERPRINT"

	// workBuddyTLSHandshakeTimeout bounds the handshake. Setting DialTLSContext
	// means Transport.TLSHandshakeTimeout no longer applies, so the bound has to
	// live here: a handshake that hangs would otherwise hold a slot forever.
	workBuddyTLSHandshakeTimeout = 10 * time.Second
)

// The measured fingerprint of the chat chain, kept as regression anchors rather
// than computed values.
const (
	// workBuddyJA3MD5 is the official chat ClientHello's JA3 md5.
	workBuddyJA3MD5 = "71dc8c533dd919ae9f4963224a4ba8fd"
	// workBuddyJA3 is the official chat ClientHello's JA3 text.
	workBuddyJA3 = "771," +
		"4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53," +
		"0-23-65281-10-11-35-13-51-45-43," +
		"29-23-24," +
		"0"
	// workBuddyJA4 is the official chat chain's JA4.
	workBuddyJA4 = "t13d0a1200_5d04281c6031_b76c5ee2507d"
)

// workBuddyOfficialCipherSuites is the official ClientHello's 18 ciphers, in
// measured order. The difference from Go's default is not just the count: the
// three TLS 1.3 suites (0x1301/1302/1303) come first here, while Go puts them
// last.
var workBuddyOfficialCipherSuites = []uint16{
	tls.TLS_AES_128_GCM_SHA256,                        // 0x1301 4865
	tls.TLS_AES_256_GCM_SHA384,                        // 0x1302 4866
	tls.TLS_CHACHA20_POLY1305_SHA256,                  // 0x1303 4867
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,         // 0xc02f 49199
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,       // 0xc02b 49195
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,         // 0xc030 49200
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,       // 0xc02c 49196
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,         // 0xc027 49191
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, // 0xcca9 52393
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,   // 0xcca8 52392
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,          // 0xc009 49161
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,            // 0xc013 49171
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,          // 0xc00a 49162
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,            // 0xc014 49172
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,               // 0x009c 156
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,               // 0x009d 157
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,                  // 0x002f 47
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,                  // 0x0035 53
}

// workBuddyOfficialSigAlgs is the official signature_algorithms, all 9 of them.
//
// JA3 cannot see these, but JA4's third segment is decided by them: one entry
// missing (ed25519, ed448, pss_pss — the client sends none of those) or one
// extra and JA4 drifts immediately. So they are copied exactly.
var workBuddyOfficialSigAlgs = []utls.SignatureScheme{
	0x0403, // ecdsa_secp256r1_sha256
	0x0804, // rsa_pss_rsae_sha256
	0x0401, // rsa_pkcs1_sha256
	0x0503, // ecdsa_secp384r1_sha384
	0x0805, // rsa_pss_rsae_sha384
	0x0501, // rsa_pkcs1_sha384
	0x0806, // rsa_pss_rsae_sha512
	0x0601, // rsa_pkcs1_sha512
	0x0201, // rsa_pkcs1_sha1
}

// workBuddyHelloSpec reproduces the official Node/OpenSSL ClientHello.
//
// Extension order is the measured order: SNI → EMS → renegotiation → groups →
// ec_point_formats → session_ticket → sigalgs → key_share → psk_modes →
// supported_versions. Getting it wrong misaligns JA3's extension segment (JA3
// uses appearance order; JA4 uses sorted order).
func workBuddyHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CipherSuites:       workBuddyOfficialCipherSuites,
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},                      // 0
			&utls.UtlsExtendedMasterSecretExtension{}, // 23
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient}, // 65281
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519,    // 29
				utls.CurveP256, // 23
				utls.CurveP384, // 24
			}}, // 10
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},                                 // 11
			&utls.SessionTicketExtension{},                                                             // 35
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: workBuddyOfficialSigAlgs}, // 13
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519},
			}}, // 51
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}}, // 45
			&utls.SupportedVersionsExtension{Versions: []uint16{
				tls.VersionTLS13,
				tls.VersionTLS12,
			}}, // 43
		},
	}
}

// workBuddyTLSFingerprintMode reads the profile selector, defaulting to the
// reproduced one. Only an explicit "go" falls back to the native shape, so
// rolling back is a configuration change rather than a code change.
func workBuddyTLSFingerprintMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(workBuddyTLSFingerprintEnv)))
	if mode == workBuddyTLSFingerprintGo {
		return workBuddyTLSFingerprintGo
	}
	return workBuddyTLSFingerprintNode
}

// workBuddyUseNodeFingerprint reports whether this mode reproduces the client's
// ClientHello.
func workBuddyUseNodeFingerprint(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) != workBuddyTLSFingerprintGo
}

// workBuddyTLSDialer builds the TLS dial hook: TCP → (fingerprinted) handshake →
// header-order wrapper.
//
// Taking over the whole TLS dial rather than just wrapping DialContext is
// deliberate: http.Transport completes the TLS handshake itself on top of
// DialContext, and what reaches the connection then is **encrypted records**, so
// request headers cannot be seen or reordered at that layer. Only a
// DialTLSContext that returns an already-handshaken connection lets the layer
// above write plaintext HTTP bytes for the header reorder to work on.
//
// The same hook carries the fingerprint: mode "node" builds the official
// ClientHello by hand, mode "go" uses crypto/tls native.
//
// The cost: once DialTLSContext is set, Transport.TLSHandshakeTimeout and
// TLSClientConfig no longer apply — the handshake timeout and ServerName are
// this function's responsibility.
//
// cfg is nil in production; tests pass a config with InsecureSkipVerify to run
// the whole path against a self-signed httptest server.
func workBuddyTLSDialer(dialer *net.Dialer, cfg *tls.Config, mode string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return workBuddyTLSHandshake(ctx, raw, addr, cfg, mode)
	}
}

// workBuddyTLSHandshake handshakes on raw under the selected profile and returns
// a plaintext connection wrapped for header reordering; raw is closed on
// failure.
func workBuddyTLSHandshake(ctx context.Context, raw net.Conn, addr string, cfg *tls.Config, mode string) (net.Conn, error) {
	if raw == nil {
		return nil, errWorkBuddyNilConn
	}
	insecure := cfg != nil && cfg.InsecureSkipVerify
	// cfg.ServerName lets a caller name the SNI explicitly (tests point the
	// connection at 127.0.0.1 but still need the real domain SNI — uTLS follows
	// the RFC and sends no SNI for an IP, which would drop an extension and move
	// both JA3 and JA4). Production passes nil and is unaffected.
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
	}
	if cfg != nil && cfg.ServerName != "" {
		host = cfg.ServerName
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, workBuddyTLSHandshakeTimeout)
	defer cancel()

	if workBuddyUseNodeFingerprint(mode) {
		// ALPN is deliberately empty: the official Node stack sends none, which
		// agrees with this transport refusing h2.
		client := utls.UClient(raw, &utls.Config{
			ServerName:         host,
			InsecureSkipVerify: insecure,
			MinVersion:         tls.VersionTLS12,
		}, utls.HelloCustom)
		if err := client.ApplyPreset(workBuddyHelloSpec()); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("workbuddy tls fingerprint preset: %w", err)
		}
		if err := client.HandshakeContext(handshakeCtx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return newWorkBuddyHeadOrderConn(client), nil
	}

	// Native path: crypto/tls unchanged.
	native := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg != nil {
		native = cfg.Clone()
	}
	native.ServerName = host
	native.NextProtos = nil
	conn := tls.Client(raw, native)
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return newWorkBuddyHeadOrderConn(conn), nil
}

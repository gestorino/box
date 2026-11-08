package relay

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	frpproxy "github.com/fatedier/frp/client/proxy"
	frplog "github.com/fatedier/frp/pkg/util/log"
	golog "github.com/fatedier/golib/log"
)

// CertManager is the tunnel's certificate side: *Cert, or a fake where no ACME may run.
type CertManager interface {
	// Manage starts getting (and keeping) a certificate for name, until ctx ends.
	Manage(ctx context.Context, name string) error
	// Ready says a valid certificate for the managed name is held.
	Ready() bool
	// Forget wipes the certificate and the ACME account. Idempotent.
	Forget() error
}

// FrpTunnel is the production Tunnel: frp's client to the relay, its one proxy to the front, and
// the front's certificate managed once the relay is reached.
type FrpTunnel struct {
	caFile string
	front  string
	cert   CertManager

	life sync.Mutex // serializes Start and Stop

	mu     sync.Mutex // guards svr, proxy, cancel, wg
	svr    *client.Service
	proxy  string
	cancel context.CancelFunc
	wg     *sync.WaitGroup
}

// NewFrpTunnel is the box's tunnel: frp's client verifying the relay against caFile, its proxy
// passing phones' TLS to front (host:port, relayd's TLS front), and cert for the front's name.
func NewFrpTunnel(caFile, front string, cert CertManager) *FrpTunnel {
	return &FrpTunnel{caFile: caFile, front: front, cert: cert}
}

var _ Tunnel = (*FrpTunnel)(nil)

// Start connects to id's relay with secret, in the background (frp logs in again by itself
// whenever the relay is lost), and manages the certificate for <name>.<box-domain> once the
// relay has the box's proxy. A tunnel already running is stopped first.
func (t *FrpTunnel) Start(id Identity, secret string) error {
	svr, err := NewFrpClient(FrpConfig{
		Server:     id.Relay,
		CAFile:     t.caFile,
		ServerName: "relay." + id.BoxDomain,
		Metas:      map[string]string{"box_id": id.BoxID, "secret": secret},
		ProxyName:  id.BoxID, // frps keeps proxy names across all boxes: the box_id is the box's own
		LocalAddr:  t.front,
	})
	if err != nil {
		return err
	}
	t.life.Lock()
	defer t.life.Unlock()
	t.stop()

	ctx, cancel := context.WithCancel(context.Background())
	wg := new(sync.WaitGroup)
	t.mu.Lock()
	t.svr, t.proxy, t.cancel, t.wg = svr, id.BoxID, cancel, wg
	t.mu.Unlock()
	wg.Go(func() {
		if err := svr.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("relay: frp client stopped", "err", err)
		}
	})
	wg.Go(func() { t.certify(ctx, id.Name+"."+id.BoxDomain) })
	return nil
}

// certify waits until the relay has the box's proxy, so the CA's check can reach the front,
// then manages the certificate.
func (t *FrpTunnel) certify(ctx context.Context, name string) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for !t.Connected() {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	if err := t.cert.Manage(ctx, name); err != nil {
		slog.Error("relay: manage the certificate", "name", name, "err", err)
	}
}

// Stop closes the connection to the relay and waits for it. Idempotent.
func (t *FrpTunnel) Stop() {
	t.life.Lock()
	defer t.life.Unlock()
	t.stop()
}

func (t *FrpTunnel) stop() {
	t.mu.Lock()
	cancel, wg := t.cancel, t.wg
	t.svr, t.proxy, t.cancel, t.wg = nil, "", nil, nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		wg.Wait()
	}
}

// Forget stops the tunnel, so no obtain is under way, then wipes the certificate side: the
// box's certificate and its ACME account.
func (t *FrpTunnel) Forget() error {
	t.Stop()
	return t.cert.Forget()
}

// Connected says the relay has the box's proxy running: phones can reach the front.
func (t *FrpTunnel) Connected() bool {
	t.mu.Lock()
	svr, name := t.svr, t.proxy
	t.mu.Unlock()
	if svr == nil {
		return false
	}
	st, ok := svr.StatusExporter().GetProxyStatus(name)
	return ok && st.Phase == frpproxy.ProxyPhaseRunning
}

// Waiting is what the box waits on, for Status: "relay", "certificate", or "" when neither.
func (t *FrpTunnel) Waiting() string {
	switch {
	case !t.Connected():
		return "relay"
	case !t.cert.Ready():
		return "certificate"
	}
	return ""
}

// frpLogLevel is the least level of frp's lines that reach slog.
var frpLogLevel = func() *slog.LevelVar { v := new(slog.LevelVar); v.Set(slog.LevelInfo); return v }()

// frp logs through one package-level logger, by default to stdout. It goes to slog instead, set
// once before anything of frp runs (the logger is read unguarded); frp formats every level and
// frpLogLevel drops what's below it. frp logs no metadatas, so the box secret is in no line
// (TestFrpLogsNeverCarryTheSecret).
func init() {
	frplog.Logger = frplog.Logger.WithOptions(golog.WithOutput(frpSlog{}), golog.WithLevel(golog.TraceLevel))
}

// frpSlog writes frp's lines to slog.Default at their level.
type frpSlog struct{}

// frpHeader is what frp puts before each line: its time and level ("2006-01-02 15:04:05.000 [I] ").
const frpHeader = len("2006-01-02 15:04:05.000 [I] ")

func (frpSlog) WriteLog(p []byte, l golog.Level, _ time.Time) (int, error) {
	lv := slog.LevelInfo
	switch l {
	case golog.TraceLevel:
		lv = slog.LevelDebug - 4
	case golog.DebugLevel:
		lv = slog.LevelDebug
	case golog.WarnLevel:
		lv = slog.LevelWarn
	case golog.ErrorLevel:
		lv = slog.LevelError
	}
	if lv < frpLogLevel.Level() {
		return len(p), nil
	}
	line := strings.TrimRight(string(p), "\n")
	if len(line) > frpHeader {
		line = line[frpHeader:]
	}
	slog.Log(context.Background(), lv, "frp: "+line)
	return len(p), nil
}

func (w frpSlog) Write(p []byte) (int, error) { return w.WriteLog(p, golog.InfoLevel, time.Time{}) }

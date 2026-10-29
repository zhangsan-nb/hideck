package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
)

// 登录限流必须按真实客户端 IP 计数，而不是按客户端可随意伪造的
// X-Forwarded-For 头计数，否则暴力破解限流形同虚设。
func TestLoginRateLimitIgnoresSpoofedForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{
		auth:          config.WebConfig{Username: "admin", Password: "correct-horse-battery-staple"},
		loginAttempts: make(map[string]loginAttempt),
	}
	r := s.newRouter()

	body := `{"username":"admin","password":"wrong"}`

	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i)) // 每次伪造不同的来源 IP
		req.RemoteAddr = "192.0.2.1:54321"                             // 真实连接对端始终相同
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code=%d want 401, body=%s", i, w.Code, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.0.0.99") // 第 11 次，依然是新的伪造 IP
	req.RemoteAddr = "192.0.2.1:54321"
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt: code=%d want 429 —— 限流应按真实 RemoteAddr 计数，不应被伪造的 X-Forwarded-For 绕过, body=%s", w.Code, w.Body.String())
	}
	if got, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || got < 590 || got > 600 {
		t.Fatalf("Retry-After=%q want about 600", w.Header().Get("Retry-After"))
	}
}

// Both handleLogin and authorizeRotate must release on a correct password, or
// scripts calling rotate with credentials get locked out.
func TestLoginAndRotateSuccessesDoNotCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{
		auth:          config.WebConfig{Username: "admin", Password: "correct-horse-battery-staple"},
		loginAttempts: make(map[string]loginAttempt),
	}
	r := s.newRouter()
	login := func(password string) int {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"username":"admin","password":%q}`, password)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.1:54321"
		r.ServeHTTP(w, req)
		return w.Code
	}
	rotate := func() bool {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/rotateip", nil)
		c.Request.RemoteAddr = "192.0.2.1:54321"
		return s.authorizeRotate(c, "admin", "correct-horse-battery-staple", time.Now())
	}

	for i := 0; i < loginAttemptLimit-1; i++ {
		if code := login("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: code=%d want 401", i, code)
		}
	}
	for i := 0; i < loginAttemptLimit; i++ {
		if code := login("correct-horse-battery-staple"); code != http.StatusOK {
			t.Fatalf("login success %d: code=%d want 200", i, code)
		}
		if !rotate() {
			t.Fatalf("rotate success %d rejected", i)
		}
	}
	if code := login("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("10th failure: code=%d want 401", code)
	}
	if code := login("wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("11th failure: code=%d want 429", code)
	}
}

func allowed(s *Server, ip string, now time.Time) bool {
	release, _ := s.allowLoginAttempt(ip, now)
	return release != nil
}

// fail reserves n attempts without releasing them, i.e. n wrong passwords.
func fail(s *Server, ip string, now time.Time, n int) {
	for i := 0; i < n; i++ {
		s.allowLoginAttempt(ip, now)
	}
}

func TestLoginSuccessesNeverLockOut(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 5*loginAttemptLimit; i++ {
		release, _ := s.allowLoginAttempt("192.0.2.1", now)
		if release == nil {
			t.Fatalf("successful attempt %d rejected", i)
		}
		release()
	}
}

// A success releases only its own reservation; other failures from the IP
// still count.
func TestLoginSuccessKeepsOtherAttemptsCounted(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	fail(s, "192.0.2.1", now, loginAttemptLimit-1)
	release, _ := s.allowLoginAttempt("192.0.2.1", now)
	release()
	if !allowed(s, "192.0.2.1", now) {
		t.Fatal("10th counted attempt rejected")
	}
	if allowed(s, "192.0.2.1", now) {
		t.Fatal("11th counted attempt allowed; a success must not refund other attempts")
	}
}

// A correct password on the attempt that reached the limit lifts its lockout
// and does not extend the counting window.
func TestLoginSuccessAtLimitDoesNotExtendWindow(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	fail(s, "192.0.2.1", now, loginAttemptLimit-1)
	release, _ := s.allowLoginAttempt("192.0.2.1", now.Add(time.Minute))
	release()
	later := now.Add(loginAttemptWindow + time.Second)
	fail(s, "192.0.2.1", later, loginAttemptLimit-1)
	if !allowed(s, "192.0.2.1", later) {
		t.Fatal("old failures still counted after their window ended")
	}
}

// A release arriving after its window ended must not refund the new window.
func TestLateReleaseDoesNotRefundNewWindow(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	release, _ := s.allowLoginAttempt("192.0.2.1", now)
	later := now.Add(loginAttemptWindow)
	fail(s, "192.0.2.1", later, loginAttemptLimit-1)
	release()
	if !allowed(s, "192.0.2.1", later) {
		t.Fatal("10th attempt in the new window rejected")
	}
	if allowed(s, "192.0.2.1", later) {
		t.Fatal("late release from the previous window refunded the new one")
	}
}

func TestLoginLockoutExpires(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	fail(s, "192.0.2.1", now, loginAttemptLimit)
	if release, wait := s.allowLoginAttempt("192.0.2.1", now.Add(time.Second)); release != nil || wait != loginLockout-time.Second {
		t.Fatalf("allowed=%v wait=%v want locked with %v left", release != nil, wait, loginLockout-time.Second)
	}
	if allowed(s, "192.0.2.1", now.Add(loginLockout-time.Second)) {
		t.Fatal("lockout ended early")
	}
	if !allowed(s, "192.0.2.1", now.Add(loginLockout)) {
		t.Fatal("still locked after lockout period")
	}
	if !allowed(s, "192.0.2.2", now) {
		t.Fatal("lockout leaked to another IP")
	}
}

func TestLoginAttemptsPruneExpiredIPs(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 100; i++ {
		s.allowLoginAttempt(fmt.Sprintf("198.51.100.%d", i), now)
	}
	fail(s, "203.0.113.1", now, loginAttemptLimit)
	s.allowLoginAttempt("192.0.2.1", now.Add(loginAttemptWindow))
	if len(s.loginAttempts) != 2 {
		t.Fatalf("len=%d want 2: expired IPs pruned, locked IP kept", len(s.loginAttempts))
	}
}

func TestCheckPasswordPlaintextFallbackStillMatchesCorrectly(t *testing.T) {
	if !checkPassword("plain-secret", "plain-secret") {
		t.Fatal("相同的明文密码应当匹配")
	}
	if checkPassword("plain-secret", "wrong-secret") {
		t.Fatal("不同明文密码不应匹配")
	}
	if checkPassword("plain-secret", "plain-secre") {
		t.Fatal("长度不同的明文密码不应匹配")
	}
	if checkPassword("plain-secret", "") {
		t.Fatal("空密码不应匹配非空存储密码")
	}
}

// In-flight password checks reserve capacity too; successful checks only
// refund their own slots, even when requests arrive at the same time.
func TestLoginConcurrentReservationsAndRefunds(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Unix(1_800_000_000, 0)
	reserveBurst := func() []func() {
		const contenders = 64
		start := make(chan struct{})
		results := make(chan func(), contenders)
		var workers sync.WaitGroup
		for range contenders {
			workers.Go(func() {
				<-start
				release, _ := s.allowLoginAttempt("192.0.2.1", now)
				results <- release
			})
		}
		close(start)
		workers.Wait()
		close(results)
		var allowed []func()
		for release := range results {
			if release != nil {
				allowed = append(allowed, release)
			}
		}
		return allowed
	}
	reserved := reserveBurst()
	if len(reserved) != loginAttemptLimit {
		t.Fatalf("reserved=%d want %d", len(reserved), loginAttemptLimit)
	}
	refunds := loginAttemptLimit / 2
	var workers sync.WaitGroup
	for _, release := range reserved[:refunds] {
		workers.Go(release)
	}
	workers.Wait()
	if got := len(reserveBurst()); got != refunds {
		t.Fatalf("reserved after refund=%d want %d", got, refunds)
	}
}

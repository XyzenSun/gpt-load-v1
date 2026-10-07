package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func TestLoggerOtherQueryAuthRedaction(t *testing.T) {
	gm := newAuthTestGroupManager(t)
	logger := logrus.StandardLogger()
	oldOutput, oldLevel, oldFormatter := logger.Out, logger.GetLevel(), logger.Formatter
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	t.Cleanup(func() {
		logger.SetOutput(oldOutput)
		logger.SetLevel(oldLevel)
		logger.SetFormatter(oldFormatter)
	})

	for _, tt := range []struct {
		name, group, query, header, loggedQuery string
		status                                  int
	}{
		{"query success", "other", "key=proxy&key=log-secret&x=1", "", "x=1", 204},
		{"query failure", "other", "key=log-secret&key=proxy&x=1", "", "x=1", 401},
		{"header preserves business key", "other", "key=business&x=%2f", "Bearer proxy", "key=business&x=%2f", 204},
		{"existing channel unchanged", "openai", "key=proxy&key=business&x=1", "", "key=proxy&key=business&x=1", 204},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output.Reset()
			router := gin.New()
			router.GET("/proxy/:group_name", Logger(types.LogConfig{}), ProxyAuth(gm), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/proxy/"+tt.group+"?"+tt.query, nil)
			req.Header.Set("Authorization", tt.header)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			logged := output.String()
			if !strings.Contains(logged, "/proxy/"+tt.group+"?"+tt.loggedQuery+" - ") {
				t.Fatalf("unexpected log: %s", logged)
			}
			if strings.Contains(logged, "log-secret") {
				t.Fatalf("consumed query credential leaked: %s", logged)
			}
		})
	}
}

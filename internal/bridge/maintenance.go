package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"time"
)

type Coordination struct{ Command string }

func maintenanceNotice(busy bool, err error) string {
	boundary := "允许讨论和只读核查。这是Bridge执行约束，不是OS权限沙箱。生产写入必须有当前用户明确授权，并重新读取正式status，仅对实际受影响checks以真实当前thread URI begin；每次写前guard，验收后end。其它owner/过期登记均不得抢占，失败保留登记；不要清锁、删checks、提权或换provider。"
	if err != nil {
		return "维护状态读取/校验失败，本轮仅可讨论与只读，不得推断空闲或生产写入。" + boundary
	}
	if busy {
		return "配置checks存在维护登记，本轮允许讨论/只读；与其它owner或过期登记重叠的实际checks禁止写入。" + boundary
	}
	return "预检暂未发现配置checks的冲突；这不产生写入授权，也不代替操作前的原子登记和guard。" + boundary
}

type limitedBuffer struct {
	bytes.Buffer
	Limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.Limit-b.Len() {
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func (c Coordination) Busy(ctx context.Context, t Target) (bool, error) {
	if len(t.Checks) == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Command, "status")
	out := &limitedBuffer{Limit: 32768}
	cmd.Stdout = out
	if cmd.Run() != nil {
		return false, errors.New("maintenance status unavailable")
	}
	return MaintenanceBusy(out.Bytes(), t.Checks)
}
func MaintenanceBusy(b []byte, checks []string) (bool, error) {
	var r struct {
		OK         bool `json:"ok"`
		Operations *[]struct {
			Check string `json:"check"`
		} `json:"operations"`
	}
	if len(b) > 32768 || json.Unmarshal(b, &r) != nil || !r.OK || r.Operations == nil {
		return false, errors.New("invalid maintenance status")
	}
	for _, op := range *r.Operations {
		if op.Check == "" {
			return false, errors.New("invalid maintenance operation")
		}
		if slices.Contains(checks, op.Check) || slices.Contains(checks, "*") || op.Check == "*" {
			return true, nil
		}
	}
	return false, nil // Expiry never grants ownership; every matching registration blocks.
}

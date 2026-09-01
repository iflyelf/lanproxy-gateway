package auth

import "testing"

func TestLoginSuccess(t *testing.T) {
	a, err := New("admin", "secret")
	if err != nil {
		t.Fatalf("创建认证器失败: %v", err)
	}
	token, ok := a.Login("admin", "secret")
	if !ok {
		t.Fatal("正确凭据应登录成功")
	}
	if !a.valid(token) {
		t.Error("签发的 token 应有效")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a, _ := New("admin", "secret")
	if _, ok := a.Login("admin", "wrong"); ok {
		t.Error("错误密码应登录失败")
	}
}

func TestLoginWrongUser(t *testing.T) {
	a, _ := New("admin", "secret")
	if _, ok := a.Login("root", "secret"); ok {
		t.Error("错误用户名应登录失败")
	}
}

func TestLogoutInvalidatesToken(t *testing.T) {
	a, _ := New("admin", "secret")
	token, _ := a.Login("admin", "secret")
	a.Logout(token)
	if a.valid(token) {
		t.Error("登出后 token 应失效")
	}
}

func TestBcryptHashPassword(t *testing.T) {
	// 预先计算的 "secret" 的 bcrypt 哈希。
	hash := "$2a$10$X1BiIBWIivDnwd6dbVnI4u7svHJrIRnSqg7SQv/6V5jsM79ZsX0IS"
	a, err := New("admin", hash)
	if err != nil {
		t.Fatalf("使用哈希创建失败: %v", err)
	}
	if _, ok := a.Login("admin", "secret"); !ok {
		t.Error("哈希模式下正确密码应登录成功")
	}
}

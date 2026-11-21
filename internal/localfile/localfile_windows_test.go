package localfile

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRejectForeignACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if published, err := WritePrivateAtomic(path, []byte("initial"), true); !published || err != nil {
		t.Fatalf("create: published=%v, err=%v", published, err)
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setACL := func(extra string) {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + tokenUser.User.Sid.String() + ")" + extra)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
	}
	setACL("(A;;FA;;;SY)(A;;FA;;;BA)")
	requirePrivateContents(t, path, "initial")
	setACL("(A;;FR;;;WD)")
	if f, err := OpenPrivate(path); !errors.Is(err, errNotPrivate) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("OpenPrivate with Everyone access: %v", err)
	}
	if published, err := WritePrivateAtomic(path, []byte("replacement"), false); !published || err != nil {
		t.Fatalf("replace: published=%v, err=%v", published, err)
	}
	requirePrivateContents(t, path, "replacement")
}

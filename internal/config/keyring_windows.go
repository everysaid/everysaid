package config

import (
	"errors"

	"github.com/danieljoos/wincred"
)

// The Credential Manager as Python's keyring (WinVaultKeyring) uses it.
func init() {
	v := winVault{wincredStore{}}
	keyringGet = v.get
	keyringSet = v.set
	keyringDelete = v.delete
}

type wincredStore struct{}

func (wincredStore) read(target string) (string, []byte, bool, error) {
	c, err := wincred.GetGenericCredential(target)
	if errors.Is(err, wincred.ErrElementNotFound) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return c.UserName, c.CredentialBlob, true, nil
}

func (wincredStore) write(target, user string, blob []byte) error {
	c := wincred.NewGenericCredential(target)
	c.UserName = user
	c.CredentialBlob = blob
	c.Persist = wincred.PersistEnterprise
	return c.Write()
}

func (wincredStore) remove(target string) error {
	c, err := wincred.GetGenericCredential(target)
	if errors.Is(err, wincred.ErrElementNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.Delete()
}

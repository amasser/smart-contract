package wallet

/**
 * Wallet Service
 *
 * What is my purpose?
 * - You store keys
 */

import (
	"context"
	"errors"
	"sync"

	"github.com/tokenized/smart-contract/internal/platform/db"
	"github.com/tokenized/smart-contract/pkg/bitcoin"
)

type WalletInterface interface {
	Get(bitcoin.RawAddress) (*Key, error)
	List([]bitcoin.RawAddress) ([]*Key, error)
	ListAll() []*Key
	Add(*Key) error
	Remove(*Key) error
	Load(context.Context, *db.DB, bitcoin.Network) error
	Save(context.Context, *db.DB, bitcoin.Network) error
}

type Wallet struct {
	lock     sync.RWMutex
	KeyStore *KeyStore
}

func New() *Wallet {
	return &Wallet{
		KeyStore: NewKeyStore(),
	}
}

func (w Wallet) Add(key *Key) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	return w.KeyStore.Add(key)
}

func (w Wallet) Remove(key *Key) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	return w.KeyStore.Remove(key)
}

// Register a private key with the wallet
func (w Wallet) Register(wif string, net bitcoin.Network) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	if len(wif) == 0 {
		return errors.New("Create wallet failed: missing secret")
	}

	// load the WIF if we have one
	key, err := bitcoin.KeyFromStr(wif)
	if err != nil {
		return err
	}

	// Put in key store
	newKey := NewKey(key)
	w.KeyStore.Add(newKey)
	return nil
}

func (w Wallet) List(addrs []bitcoin.RawAddress) ([]*Key, error) {
	w.lock.RLock()
	defer w.lock.RUnlock()

	var rks []*Key

	for _, addr := range addrs {
		rk, err := w.Get(addr)
		if err != nil {
			if err == ErrKeyNotFound {
				continue
			}
			return nil, err
		}

		rks = append(rks, rk)
	}

	return rks, nil
}

func (w Wallet) ListAll() []*Key {
	w.lock.RLock()
	defer w.lock.RUnlock()
	return w.KeyStore.GetAll()
}

func (w Wallet) Get(address bitcoin.RawAddress) (*Key, error) {
	w.lock.RLock()
	defer w.lock.RUnlock()

	return w.KeyStore.Get(address)
}

func (w Wallet) Load(ctx context.Context, masterDB *db.DB, net bitcoin.Network) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	return w.KeyStore.Load(ctx, masterDB, net)
}

func (w Wallet) Save(ctx context.Context, masterDB *db.DB, net bitcoin.Network) error {
	w.lock.RLock()
	defer w.lock.RUnlock()

	return w.KeyStore.Save(ctx, masterDB)
}

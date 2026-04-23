package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	sqld "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Account represents an Ethereum account stored in the database.
type Account struct {
	PrivateKey string
	PublicKey  string
	Address    string
	Balance    float64
	gorm.Model
}

// Environment holds all runtime configuration from environment variables.
type Environment struct {
	DBUsername     string `env:"DB_USERNAME,required"`
	DBPassword     string `env:"DB_PASSWORD,required"`
	DBHost         string `env:"DB_HOST,required"`
	DBPort         int    `env:"DB_PORT,required"`
	DBSchema       string `env:"DB_SCHEMA,required"`
	ServerPort     int    `env:"SERVER_PORT,required"`
	EthNodeURL     string `env:"ETH_NODE_URL,required"`
	BatchSize      int    `env:"BATCH_SIZE" envDefault:"500"`
	RPCConcurrency int    `env:"RPC_CONCURRENCY" envDefault:"32"`
	KeygenWorkers  int    `env:"KEYGEN_WORKERS" envDefault:"0"`
}

// keyInfo holds the generated key material for a single Ethereum address.
type keyInfo struct {
	privateKey     *ecdsa.PrivateKey
	publicKeyBytes []byte
	address        string
}

// databaseConfig holds the database connection parameters.
type databaseConfig struct {
	Username string
	Password string
	Address  string
	Port     int
	Name     string
}

func main() {
	var e Environment
	if err := env.Parse(&e); err != nil {
		logrus.WithError(err).Panic("cannot configure environment variables")
	}
	if e.KeygenWorkers <= 0 {
		e.KeygenWorkers = runtime.NumCPU()
	}

	db := initDatabase(databaseConfig{
		Username: e.DBUsername,
		Password: e.DBPassword,
		Address:  e.DBHost,
		Port:     e.DBPort,
		Name:     e.DBSchema,
	})
	sqlDB, _ := db.DB()

	// Connect to the local Ethereum node (IPC path, ws://, or http://).
	// rpc.Dial selects the transport based on the URL prefix.
	rpcClient, err := ethrpc.Dial(e.EthNodeURL)
	if err != nil {
		logrus.WithError(err).Panicf("cannot connect to Ethereum node at %s", e.EthNodeURL)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Capture SIGINT / SIGTERM for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	keygenCh := make(chan keyInfo, e.BatchSize*e.RPCConcurrency*2)
	resultCh := make(chan Account, 256)

	var wgKeygen sync.WaitGroup
	var wgBatcher sync.WaitGroup
	var wgWriter sync.WaitGroup

	var totalKeys atomic.Int64
	var totalHits atomic.Int64
	var rpcErrors atomic.Int64
	var prevKeys int64

	startTime := time.Now()

	// --- Keygen workers ---
	for i := 0; i < e.KeygenWorkers; i++ {
		wgKeygen.Add(1)
		go func() {
			defer wgKeygen.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ki, genErr := generateKeyInfo()
				if genErr != nil {
					logrus.WithError(genErr).Error("error generating key")
					continue
				}
				select {
				case keygenCh <- ki:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Close keygenCh once all keygen workers have exited.
	go func() {
		wgKeygen.Wait()
		close(keygenCh)
	}()

	// --- Batcher workers ---
	for i := 0; i < e.RPCConcurrency; i++ {
		wgBatcher.Add(1)
		go func() {
			defer wgBatcher.Done()
			batchWorker(ctx, rpcClient, keygenCh, resultCh, e.BatchSize, &totalKeys, &rpcErrors)
		}()
	}

	// Close resultCh once all batcher workers have exited.
	go func() {
		wgBatcher.Wait()
		close(resultCh)
	}()

	// --- DB writer ---
	wgWriter.Add(1)
	go func() {
		defer wgWriter.Done()
		for account := range resultCh {
			totalHits.Add(1)
			logrus.WithFields(logrus.Fields{
				"private_key": account.PrivateKey,
				"public_key":  account.PublicKey,
				"address":     account.Address,
				"balance":     account.Balance,
			}).Info("found account with balance")
			if dbErr := db.Create(&account).Error; dbErr != nil {
				logrus.WithError(dbErr).Error("error saving account to database")
			}
		}
	}()

	// --- Metrics ticker ---
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				now := totalKeys.Load()
				elapsed := time.Since(startTime).Seconds()
				avg := float64(now) / elapsed
				inst := float64(now-prevKeys) / (2 * 60)
				prevKeys = now
				fmt.Printf(
					"[metrics] keys/s (inst=%.0f avg=%.0f) total=%d hits=%d rpc_errors=%d\n",
					inst, avg, now, totalHits.Load(), rpcErrors.Load(),
				)
			case <-ctx.Done():
				return
			}
		}
	}()

	// --- HTTP health endpoint ---
	router := mux.NewRouter()
	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"running"}`))
	})
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", e.ServerPort),
		Handler: router,
	}
	go func() {
		if srvErr := srv.ListenAndServe(); srvErr != nil && srvErr != http.ErrServerClosed {
			logrus.WithError(srvErr).Error("HTTP server error")
		}
	}()

	// Wait for shutdown signal.
	<-sigCh
	logrus.Info("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	wgKeygen.Wait()
	wgBatcher.Wait()
	wgWriter.Wait()

	rpcClient.Close()
	if sqlDB != nil {
		_ = sqlDB.Close()
	}

	logrus.WithFields(logrus.Fields{
		"total_keys": totalKeys.Load(),
		"total_hits": totalHits.Load(),
	}).Info("shutdown complete")
}

// batchWorker collects keyInfo items from keygenCh in batches and issues a single
// BatchCallContext request per batch against the Ethereum node.
func batchWorker(
	ctx context.Context,
	rpcClient *ethrpc.Client,
	keygenCh <-chan keyInfo,
	resultCh chan<- Account,
	batchSize int,
	totalKeys *atomic.Int64,
	rpcErrors *atomic.Int64,
) {
	batch := make([]keyInfo, 0, batchSize)
	timeout := time.NewTimer(50 * time.Millisecond)
	defer timeout.Stop()

	flush := func() {
		if len(batch) == 0 || ctx.Err() != nil {
			batch = batch[:0]
			return
		}
		queryBatch(ctx, rpcClient, batch, resultCh, totalKeys, rpcErrors)
		batch = batch[:0]
		if !timeout.Stop() {
			select {
			case <-timeout.C:
			default:
			}
		}
		timeout.Reset(50 * time.Millisecond)
	}

	for {
		select {
		case ki, ok := <-keygenCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ki)
			if len(batch) >= batchSize {
				flush()
			}
		case <-timeout.C:
			flush()
			timeout.Reset(50 * time.Millisecond)
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// queryBatch sends a single JSON-RPC batch request for eth_getBalance on all
// provided addresses and forwards accounts with non-zero balance to resultCh.
// It retries on transient errors (HTTP 429, connection reset, JSON-RPC -32005)
// using exponential backoff with jitter (max 5 attempts).
func queryBatch(
	ctx context.Context,
	rpcClient *ethrpc.Client,
	keys []keyInfo,
	resultCh chan<- Account,
	totalKeys *atomic.Int64,
	rpcErrors *atomic.Int64,
) {
	const maxRetries = 5
	const baseDelay = 500 * time.Millisecond
	const maxDelay = 30 * time.Second

	elems := make([]ethrpc.BatchElem, len(keys))
	results := make([]*string, len(keys))
	for i := range keys {
		s := new(string)
		results[i] = s
		elems[i] = ethrpc.BatchElem{
			Method: "eth_getBalance",
			Args:   []interface{}{keys[i].address, "latest"},
			Result: s,
		}
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return
		}

		err := rpcClient.BatchCallContext(ctx, elems)
		if err != nil {
			if isRateLimitError(err) || isTransientError(err) {
				delay := backoffDelay(attempt, baseDelay, maxDelay)
				logrus.WithFields(logrus.Fields{
					"attempt": attempt + 1,
					"delay":   delay,
					"error":   err,
				}).Warn("RPC batch error, retrying with backoff")
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
				continue
			}
			rpcErrors.Add(1)
			return
		}

		// Check for per-element rate-limit errors and retry the whole batch.
		hasRateLimit := false
		for _, elem := range elems {
			if elem.Error != nil && isRateLimitError(elem.Error) {
				hasRateLimit = true
				break
			}
		}
		if hasRateLimit {
			// Reset per-element errors so we retry all elements.
			for i := range elems {
				elems[i].Error = nil
				s := new(string)
				results[i] = s
				elems[i].Result = s
			}
			delay := backoffDelay(attempt, baseDelay, maxDelay)
			logrus.WithFields(logrus.Fields{
				"attempt": attempt + 1,
				"delay":   delay,
			}).Warn("RPC rate limit hit, retrying batch with backoff")
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			continue
		}

		// Batch call succeeded — process results.
		totalKeys.Add(int64(len(keys)))
		for i, elem := range elems {
			if elem.Error != nil {
				rpcErrors.Add(1)
				continue
			}
			hexStr := *results[i]
			wei := parseHexBalance(hexStr)
			if wei == nil || wei.Sign() == 0 {
				continue
			}
			ki := keys[i]
			resultCh <- Account{
				PrivateKey: hexutil.Encode(crypto.FromECDSA(ki.privateKey)),
				PublicKey:  hexutil.Encode(ki.publicKeyBytes),
				Address:    ki.address,
				Balance:    weiToEther(wei),
			}
		}
		return
	}

	// All retries exhausted — log and discard the batch.
	rpcErrors.Add(1)
	logrus.WithField("batch_size", len(keys)).Warn("batch discarded after max retries")
}

// isRateLimitError returns true for HTTP 429 responses and JSON-RPC error codes
// that indicate rate limiting (-32005 "limit exceeded" or -32029).
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, keyword := range []string{"-32005", "-32029", "429", "too many requests", "rate limit", "rate-limit", "limit exceeded"} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// isTransientError returns true for errors that are likely temporary and worth retrying.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, keyword := range []string{"connection reset", "eof", "broken pipe", "timeout", "context deadline exceeded", "i/o timeout"} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// backoffDelay computes an exponential backoff duration with ±25% jitter.
func backoffDelay(attempt int, base, max time.Duration) time.Duration {
	exp := base
	for i := 0; i < attempt; i++ {
		exp *= 2
		if exp > max {
			exp = max
			break
		}
	}
	// Add ±25% jitter.
	jitter := time.Duration(rand.Int63n(int64(exp/2))) - exp/4
	d := exp + jitter
	if d < base {
		d = base
	}
	if d > max {
		d = max
	}
	return d
}

// generateKeyInfo generates a new Ethereum key pair and returns the associated keyInfo.
func generateKeyInfo() (keyInfo, error) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		return keyInfo{}, err
	}
	publicKeyECDSA, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		return keyInfo{}, fmt.Errorf("unexpected public key type")
	}
	return keyInfo{
		privateKey:     privateKey,
		publicKeyBytes: crypto.FromECDSAPub(publicKeyECDSA),
		address:        crypto.PubkeyToAddress(*publicKeyECDSA).Hex(),
	}, nil
}

// parseHexBalance parses a hex-encoded wei string (e.g. "0x1a") into a big.Int.
// Returns nil for empty, "0x0", or unparseable values.
func parseHexBalance(hexStr string) *big.Int {
	if hexStr == "" || hexStr == "0x0" || hexStr == "0x" {
		return nil
	}
	raw := hexStr
	if len(raw) >= 2 && (raw[:2] == "0x" || raw[:2] == "0X") {
		raw = raw[2:]
	}
	if raw == "" || raw == "0" {
		return nil
	}
	wei := new(big.Int)
	if _, ok := wei.SetString(raw, 16); !ok {
		return nil
	}
	return wei
}

// weiPerEther is 10^18, computed once at startup.
var weiPerEther = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// weiToEther converts a wei amount to ether as float64.
func weiToEther(wei *big.Int) float64 {
	if wei == nil {
		return 0
	}
	result, _ := new(big.Float).Quo(
		new(big.Float).SetInt(wei),
		new(big.Float).SetInt(weiPerEther),
	).Float64()
	return result
}

func initDatabase(dbConfig databaseConfig) *gorm.DB {
	configDB := mysql.Config{
		User:                 dbConfig.Username,
		Passwd:               dbConfig.Password,
		Addr:                 fmt.Sprintf("%s:%d", dbConfig.Address, dbConfig.Port),
		Net:                  "tcp",
		DBName:               dbConfig.Name,
		Loc:                  time.UTC,
		ParseTime:            true,
		AllowNativePasswords: true,
	}

	db, err := gorm.Open(sqld.Open(configDB.FormatDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}

	if err = db.AutoMigrate(&Account{}); err != nil {
		panic(err)
	}
	return db
}

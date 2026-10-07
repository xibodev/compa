package dashboardauth

const (
	// DBFilename is the SQLite database file stored under the Compa home directory.
	DBFilename = "launcher-auth.db"

	sqliteDriver = "sqlite"
	// bcryptCost is deliberately high enough to slow brute-force attempts.
	bcryptCost = 12

	sqlCreateTable = `
		CREATE TABLE IF NOT EXISTS dashboard_credentials (
			id          INTEGER PRIMARY KEY CHECK (id = 1),
			bcrypt_hash TEXT    NOT NULL
		)`

	sqlCountCredentials = `SELECT COUNT(*) FROM dashboard_credentials WHERE id = 1`

	sqlUpsertHash = `
		INSERT INTO dashboard_credentials (id, bcrypt_hash) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET bcrypt_hash = excluded.bcrypt_hash`

	// sqlInsertFirstHash stores the first hash only: a row that exists
	// already stays, and the insert affects no row.
	sqlInsertFirstHash = `
		INSERT INTO dashboard_credentials (id, bcrypt_hash) VALUES (1, ?)
		ON CONFLICT(id) DO NOTHING`

	// sqlBusyTimeout lets a write wait for another process (the -password
	// command) instead of failing at once.
	sqlBusyTimeout = `PRAGMA busy_timeout = 5000`

	sqlSelectHash = `SELECT bcrypt_hash FROM dashboard_credentials WHERE id = 1`
)

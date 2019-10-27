package pgengine_test

import (
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/ory/dockertest"
	"github.com/ory/dockertest/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pg_timetable/internal/pgengine"
	"github.com/cybertec-postgresql/pg_timetable/internal/tasks"
)

var pgURL *url.URL

// setup environment variable runDocker to true to run testcases using postgres docker images
var runDocker bool

func TestMain(m *testing.M) {
	pgengine.LogToDB("LOG", "Starting TestMain...")
	code := 0
	defer func() {
		os.Exit(code)
	}()

	runDocker, _ = strconv.ParseBool(os.Getenv("RUN_DOCKER"))
	//Create Docker image and run postgres docker image
	if runDocker {
		pgURL = &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword("scheduler", "scheduler"),
			Path:   "timetable",
		}
		q := pgURL.Query()
		q.Add("sslmode", "disable")
		pgURL.RawQuery = q.Encode()

		pool, err := dockertest.NewPool("")
		if err != nil {
			pgengine.LogToDB("PANIC", "Could not connect to docker")
		}

		pw, _ := pgURL.User.Password()
		runOpts := dockertest.RunOptions{
			Repository: "postgres",
			Tag:        "latest",
			Env: []string{
				"POSTGRES_USER=" + pgURL.User.Username(),
				"POSTGRES_PASSWORD=" + pw,
				"POSTGRES_DB=" + pgURL.Path,
			},
		}

		resource, err := pool.RunWithOptions(&runOpts)
		if err != nil {
			pgengine.LogToDB("PANIC", "Could start postgres container")
		}
		defer func() {
			err = pool.Purge(resource)
			if err != nil {
				pgengine.LogToDB("PANIC", "Could not purge resource")
			}
		}()

		pgURL.Host = resource.Container.NetworkSettings.IPAddress

		// Docker layer network is different on Mac
		if runtime.GOOS == "darwin" {
			pgURL.Host = net.JoinHostPort(resource.GetBoundIP("5432/tcp"), resource.GetPort("5432/tcp"))
		}

		logWaiter, err := pool.Client.AttachToContainerNonBlocking(docker.AttachToContainerOptions{
			Container: resource.Container.ID,
			// OutputStream: log.Writer(),
			// ErrorStream:  log.Writer(),
			Stderr: true,
			Stdout: true,
			Stream: true,
		})
		if err != nil {
			pgengine.LogToDB("PANIC", "Could not connect to postgres container log output")
		}

		defer func() {
			err = logWaiter.Close()
			if err != nil {
				pgengine.LogToDB("ERROR", "Could not close container log")
			}
			err = logWaiter.Wait()
			if err != nil {
				pgengine.LogToDB("ERROR", "Could not wait for container log to close")
			}
		}()

		pool.MaxWait = 10 * time.Second
		err = pool.Retry(func() error {
			db, err := sqlx.Open("postgres", pgURL.String())
			if err != nil {
				return err
			}
			return db.Ping()
		})
		if err != nil {
			pgengine.LogToDB("PANIC", "Could not connect to postgres server")
		}
	}
	code = m.Run()
}

// setupTestDBFunc used to conect and to initialize test PostgreSQL database
var setupTestDBFunc = func() {
	pgengine.Host = "localhost"
	pgengine.Port = "5432"
	pgengine.DbName = "timetable"
	pgengine.User = "scheduler"
	pgengine.Password = "somestrong"
	pgengine.ClientName = "go-test"
	pgengine.SSLMode = "disable"
	if runDocker {
		pgengine.Host = pgURL.Hostname()
		pgengine.Port = pgURL.Port()
		pgengine.User = pgURL.User.Username()
		pgengine.Password, _ = pgURL.User.Password()
	}
	pgengine.InitAndTestConfigDBConnection(pgengine.SQLSchemaFiles)
}

func setupTestCase(t *testing.T) func(t *testing.T) {
	pgengine.ClientName = "pgengine_unit_test"
	t.Log("Setup test case")
	setupTestDBFunc()
	return func(t *testing.T) {
		pgengine.ConfigDb.MustExec("DROP SCHEMA IF EXISTS timetable CASCADE")
		t.Log("Test schema dropped")
	}
}

// setupTestRenoteDBFunc used to connect to remote postgreSQL database
var setupTestRemoteDBFunc = func() (*sqlx.DB, *sqlx.Tx) {
	connstr := fmt.Sprintf("host='%s' port='%s' sslmode='%s' dbname='%s' user='%s' password='%s'",
		pgengine.Host, pgengine.Port, pgengine.SSLMode, pgengine.DbName, pgengine.User, pgengine.Password)
	return pgengine.GetRemoteDBTransaction(connstr)
}

func TestBootstrapSQLFileExists(t *testing.T) {
	for _, f := range pgengine.SQLSchemaFiles {
		assert.FileExists(t, f, "Bootstrap file doesn't exist")
	}
}

func TestCreateConfigDBSchemaWithoutFile(t *testing.T) {
	assert.Error(t, pgengine.CreateConfigDBSchema("wrong path"), "Should error with nonexistent file")
}

func TestInitAndTestConfigDBConnection(t *testing.T) {
	teardownTestCase := setupTestCase(t)
	defer teardownTestCase(t)

	require.NotNil(t, pgengine.ConfigDb, "ConfigDB should be initialized")

	t.Run("Check timetable tables", func(t *testing.T) {
		var oid int
		tableNames := []string{"database_connection", "base_task", "task_chain",
			"chain_execution_config", "chain_execution_parameters",
			"log", "execution_log", "run_status"}
		for _, tableName := range tableNames {
			err := pgengine.ConfigDb.Get(&oid, fmt.Sprintf("SELECT COALESCE(to_regclass('timetable.%s'), 0) :: int", tableName))
			assert.NoError(t, err, fmt.Sprintf("Query for %s existance failed", tableName))
			assert.NotEqual(t, pgengine.InvalidOid, oid, fmt.Sprintf("timetable.%s function doesn't exist", tableName))
		}
	})

	t.Run("Check timetable functions", func(t *testing.T) {
		var oid int
		funcNames := []string{"_validate_json_schema_type(text, jsonb)",
			"validate_json_schema(jsonb, jsonb, jsonb)",
			"get_running_jobs(bigint)",
			"trig_chain_fixer()",
			"check_task(bigint)"}
		for _, funcName := range funcNames {
			err := pgengine.ConfigDb.Get(&oid, fmt.Sprintf("SELECT COALESCE(to_regprocedure('timetable.%s'), 0) :: int", funcName))
			assert.NoError(t, err, fmt.Sprintf("Query for %s existance failed", funcName))
			assert.NotEqual(t, pgengine.InvalidOid, oid, fmt.Sprintf("timetable.%s function doesn't exist", funcName))
		}
	})

	t.Run("Check log facility", func(t *testing.T) {
		var count int
		logLevels := []string{"DEBUG", "NOTICE", "LOG", "ERROR", "PANIC"}
		for _, pgengine.VerboseLogLevel = range []bool{true, false} {
			pgengine.ConfigDb.MustExec("TRUNCATE timetable.log")
			for _, logLevel := range logLevels {
				assert.NotPanics(t, func() {
					pgengine.LogToDB(logLevel, logLevel)
				}, "LogToDB panicked")

				if !pgengine.VerboseLogLevel {
					switch logLevel {
					case "DEBUG", "NOTICE", "LOG":
						continue
					}
				}
				err := pgengine.ConfigDb.Get(&count, "SELECT count(1) FROM timetable.log WHERE log_level = $1 AND message = $2",
					logLevel, logLevel)
				assert.NoError(t, err, fmt.Sprintf("Query for %s log entry failed", logLevel))
				assert.Equal(t, 1, count, fmt.Sprintf("%s log entry doesn't exist", logLevel))
			}
		}
	})

	t.Run("Check connection closing", func(t *testing.T) {
		pgengine.FinalizeConfigDBConnection()
		assert.Nil(t, pgengine.ConfigDb, "Connection isn't closed properly")
		// reinit connection to execute teardown actions
		setupTestDBFunc()
	})

	t.Run("Check Reconnecting Database", func(t *testing.T) {
		assert.NotPanics(t, pgengine.ReconnectDbAndFixLeftovers, "Does not panics")
	})

	t.Run("Check TryLockClientName()", func(t *testing.T) {
		assert.Equal(t, true, pgengine.TryLockClientName(), "Should succeed for clean database")
	})

	t.Run("Check SetupCloseHandler function", func(t *testing.T) {
		assert.NotPanics(t, pgengine.SetupCloseHandler, "Setup Close handler failed")
	})
}

func TestSchedulerFunctions(t *testing.T) {
	teardownTestCase := setupTestCase(t)
	defer teardownTestCase(t)

	t.Run("Check FixSchedulerCrash function", func(t *testing.T) {
		assert.NotPanics(t, pgengine.FixSchedulerCrash, "Fix scheduler crash failed")
	})

	t.Run("Check CanProceedChainExecution funtion", func(t *testing.T) {
		assert.Equal(t, true, pgengine.CanProceedChainExecution(0, 0), "Should proceed with clean database")
	})

	t.Run("Check DeleteChainConfig funtion", func(t *testing.T) {
		tx := pgengine.StartTransaction()
		assert.Equal(t, false, pgengine.DeleteChainConfig(tx, 0), "Should not delete in clean database")
		pgengine.MustCommitTransaction(tx)
	})

	t.Run("Check GetChainElements funtion", func(t *testing.T) {
		var chains []pgengine.ChainElementExecution
		tx := pgengine.StartTransaction()
		assert.True(t, pgengine.GetChainElements(tx, &chains, 0), "Should no error in clean database")
		assert.Empty(t, chains, "Should be empty in clean database")
		pgengine.MustCommitTransaction(tx)
	})

	t.Run("Check GetChainParamValues funtion", func(t *testing.T) {
		var paramVals []string
		tx := pgengine.StartTransaction()
		assert.True(t, pgengine.GetChainParamValues(tx, &paramVals, &pgengine.ChainElementExecution{
			ChainID:     0,
			ChainConfig: 0}), "Should no error in clean database")
		assert.Empty(t, paramVals, "Should be empty in clean database")
		pgengine.MustCommitTransaction(tx)
	})

	t.Run("Check InsertChainRunStatus funtion", func(t *testing.T) {
		var id int
		tx := pgengine.StartTransaction()
		assert.NotPanics(t, func() { id = pgengine.InsertChainRunStatus(tx, 0, 0) }, "Should no error in clean database")
		assert.NotZero(t, id, "Run status id should be greater then 0")
		pgengine.MustCommitTransaction(tx)
	})

	t.Run("Check Remote DB Connection string", func(t *testing.T) {
		var databaseConnection sql.NullString
		tx := pgengine.StartTransaction()
		assert.NotNil(t, pgengine.GetConnectionString(databaseConnection), "Should no error in clean database")
		pgengine.MustCommitTransaction(tx)
	})

	t.Run("Check ExecuteSQLCommand function", func(t *testing.T) {
		tx := pgengine.StartTransaction()
		assert.Error(t, pgengine.ExecuteSQLCommand(tx, "", nil), "Should error for empty script")
		assert.Error(t, pgengine.ExecuteSQLCommand(tx, " 	", nil), "Should error for whitespace only script")
		assert.NoError(t, pgengine.ExecuteSQLCommand(tx, ";", nil), "Simple query with nil as parameters argument")
		assert.NoError(t, pgengine.ExecuteSQLCommand(tx, ";", []string{}), "Simple query with empty slice as parameters argument")
		assert.NoError(t, pgengine.ExecuteSQLCommand(tx, "SELECT $1", []string{"[42]"}), "Simple query with non empty parameters")
		assert.NoError(t, pgengine.ExecuteSQLCommand(tx, "SELECT $1", []string{"[42]", `["hey"]`}), "Simple query with doubled parameters")
		assert.NoError(t, pgengine.ExecuteSQLCommand(tx, "SELECT $1, $2", []string{`[42, "hey"]`}), "Simple query with two parameters")

		pgengine.MustCommitTransaction(tx)
	})

}

func TestBuiltInTasks(t *testing.T) {
	teardownTestCase := setupTestCase(t)
	defer teardownTestCase(t)
	t.Run("Check built-in tasks number", func(t *testing.T) {
		var num int
		err := pgengine.ConfigDb.Get(&num, "SELECT count(1) FROM timetable.base_task WHERE kind = 'BUILTIN'")
		assert.NoError(t, err, "Query for built-in tasks existance failed")
		assert.Equal(t, len(tasks.Tasks), num, fmt.Sprintf("Wrong number of built-in tasks: %d", num))
	})
}

func TestGetRemoteDBTransaction(t *testing.T) {
	teardownTestCase := setupTestCase(t)
	defer teardownTestCase(t)

	remoteDb, tx := setupTestRemoteDBFunc()
	defer pgengine.FinalizeRemoteDBConnection(remoteDb)

	require.NotNil(t, remoteDb, "remoteDB should be initialized")

	t.Run("Check connection closing", func(t *testing.T) {
		pgengine.FinalizeRemoteDBConnection(remoteDb)
		assert.NotNil(t, remoteDb, "Connection isn't closed properly")
	})

	t.Run("Check set role function", func(t *testing.T) {
		var runUID sql.NullString
		runUID.String = pgengine.User
		assert.NotPanics(t, func() { pgengine.SetRole(tx, runUID) }, "Set Role failed")
	})

	t.Run("Check reset role function", func(t *testing.T) {
		assert.NotPanics(t, func() { pgengine.ResetRole(tx) }, "Reset Role failed")
	})

	pgengine.MustCommitTransaction(tx)
}

func init() {
	pgengine.PrefixSchemaFiles("../../sql/")
}

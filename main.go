package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/go-resty/resty/v2"
	"github.com/gookit/color"
	"github.com/jmoiron/sqlx"
	"github.com/rifflock/lfshook"
	log "github.com/sirupsen/logrus"
	"github.com/unkmonster/tmd/internal/database"
	"github.com/unkmonster/tmd/internal/downloading"
	"github.com/unkmonster/tmd/internal/report"
	"github.com/unkmonster/tmd/internal/targets"
	"github.com/unkmonster/tmd/internal/twitter"
	"github.com/unkmonster/tmd/internal/utils"
	"gopkg.in/yaml.v3"
)

type Cookie struct {
	AuthCoken string `yaml:"auth_token"`
	Ct0       string `yaml:"ct0"`
}

type Config struct {
	RootPath string `yaml:"root_path"`
	// StatePath is where the program keeps its own bookkeeping (the SQLite
	// database and the failed-tweet queue). It defaults to <root_path>/.data.
	// Pointing it at a local volume is the safe choice when the media live on a
	// network share: SQLite needs reliable file locking, which NFS and SMB do
	// not provide.
	StatePath          string `yaml:"state_path"`
	Cookie             Cookie `yaml:"cookie"`
	MaxDownloadRoutine int    `yaml:"max_download_routine"`
}

type userArgs struct {
	id         []uint64
	screenName []string
}

// configDir is where conf.yaml, additional_cookies.yaml, the target list and
// the reports live. It is a variable so that a container can point it at a
// mounted volume via TMD_CONFIG_DIR.
var configDir string

// targetsPath is the target list to read, if any. Empty means "none".
var targetsPath string

// targetOrder records the requested targets in input order, so the report can
// name an account even when it was never resolved.
var targetOrder []targets.Target

// targetLabelByUserID maps a resolved account back to the target that named it.
// The two labels differ (`@handle` versus the entity's own title), and the
// per-account statistics are keyed by the entity, so the mapping is needed to
// attribute them.
var targetLabelByUserID = make(map[uint64]string)

// targetReport accumulates per-account outcomes for targets_report.tsv.
var targetReport = report.New()

// configPath resolves a file inside the configuration directory.
func configPath(name string) string {
	return filepath.Join(configDir, name)
}

func (u *userArgs) GetUser(ctx context.Context, client *resty.Client) ([]*twitter.User, error) {
	users := []*twitter.User{}
	for _, id := range u.id {
		usr, err := twitter.GetUserById(ctx, client, id)
		if err != nil {
			return nil, err
		}
		users = append(users, usr)
	}

	for _, screenName := range u.screenName {
		usr, err := twitter.GetUserByScreenName(ctx, client, screenName)
		if err != nil {
			return nil, err
		}
		users = append(users, usr)
	}
	return users, nil
}

func (u *userArgs) Set(str string) error {
	if u.id == nil {
		u.id = make([]uint64, 0)
		u.screenName = make([]string, 0)
	}

	id, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		str, _ := strings.CutPrefix(str, "@")
		u.screenName = append(u.screenName, str)
	} else {
		u.id = append(u.id, id)
	}
	return nil
}

func (u *userArgs) String() string {
	return "string"
}

type intArgs struct {
	id []uint64
}

func (l *intArgs) Set(str string) error {
	if l.id == nil {
		l.id = make([]uint64, 0)
	}

	id, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return err
	}
	l.id = append(l.id, id)
	return nil
}

func (a *intArgs) String() string {
	return "string array"
}

type ListArgs struct {
	intArgs
}

func (l ListArgs) GetList(ctx context.Context, client *resty.Client) ([]*twitter.List, error) {
	lists := []*twitter.List{}
	for _, id := range l.id {
		list, err := twitter.GetLst(ctx, client, id)
		if err != nil {
			return nil, err
		}
		lists = append(lists, list)
	}
	return lists, nil
}

type Task struct {
	users []*twitter.User
	lists []twitter.ListBase
}

// resolveTarget looks up one target and records the outcome. It returns nil for
// any account that cannot be used, so that one suspended account never stops the
// rest of the list from being crawled.
func resolveTarget(ctx context.Context, client *resty.Client, t targets.Target) *twitter.User {
	label := t.Label()

	var (
		user *twitter.User
		err  error
	)
	if t.Kind == targets.KindUserID {
		id, parseErr := strconv.ParseUint(t.Value, 10, 64)
		if parseErr != nil {
			err = fmt.Errorf("invalid user id %q: %w", t.Value, parseErr)
		} else {
			user, err = twitter.GetUserById(ctx, client, id)
		}
	} else {
		user, err = twitter.GetUserByScreenName(ctx, client, t.Value)
	}

	if err != nil {
		reason := targets.Classify(err)
		if reason.Certain() {
			log.Warnf("skipping %s: %s", label, reason)
			targetReport.Skipped(label, t.Value, reason, err.Error())
		} else {
			log.Errorf("could not resolve %s (%s), skipping: %v", label, reason, err)
			targetReport.Failed(label, t.Value, reason, err)
		}
		return nil
	}

	// Expected, definitive reasons to skip an account outright.
	if user.Blocking || user.Muting {
		log.Warnf("skipping %s: blocked or muted", label)
		targetReport.Skipped(label, t.Value, targets.ReasonBlocked, "blocked or muted")
		return nil
	}
	if user.IsProtected && user.Followstate != twitter.FS_FOLLOWING {
		log.Warnf("skipping %s: protected and not followed", label)
		targetReport.Skipped(label, t.Value, targets.ReasonProtected, "protected and not followed")
		return nil
	}

	if label != user.Title() {
		log.Infof("resolved %s -> %s", label, user.Title())
	}
	targetLabelByUserID[user.Id] = label
	return user
}

// MakeTask collects what to crawl. Failures are per-target: the returned error
// is reserved for problems that make the whole run meaningless.
func MakeTask(ctx context.Context, client *resty.Client, usrArgs userArgs, listArgs ListArgs, follArgs userArgs) (*Task, error) {
	task := Task{}
	task.users = make([]*twitter.User, 0)
	task.lists = make([]twitter.ListBase, 0)

	// 1. Accounts from the target list file, if one is present.
	fileTargets, err := targets.ParseFile(targetsPath)
	if err != nil {
		return nil, err
	}
	if len(fileTargets) != 0 {
		log.Infof("loaded %d targets from %s", len(fileTargets), targetsPath)
	}
	targetOrder = append(targetOrder, fileTargets...)

	// 2. Accounts from the command line, merged with the file.
	for _, id := range usrArgs.id {
		targetOrder = append(targetOrder, targets.Target{Kind: targets.KindUserID, Value: strconv.FormatUint(id, 10)})
	}
	for _, name := range usrArgs.screenName {
		targetOrder = append(targetOrder, targets.Target{Kind: targets.KindScreenName, Value: name})
	}
	// The same account may be named twice (file and command line, or both an id
	// and a handle); crawling it twice would only duplicate work.
	targetOrder = targets.Dedupe(targetOrder)

	for _, t := range targetOrder {
		if user := resolveTarget(ctx, client, t); user != nil {
			task.users = append(task.users, user)
		}
	}

	// 3. Lists are addressed by id and can only be validated by fetching them.
	lists, err := listArgs.GetList(ctx, client)
	if err != nil {
		return nil, err
	}
	for _, list := range lists {
		task.lists = append(task.lists, list)
	}

	// 4. Following lists of the named accounts. A failure here is per-account:
	// skip it and keep the other lists.
	for _, id := range follArgs.id {
		user, err := twitter.GetUserById(ctx, client, id)
		if err != nil {
			log.Errorf("could not resolve the following list of %d, skipping: %v", id, err)
			continue
		}
		task.lists = append(task.lists, user.Following())
	}
	for _, screenName := range follArgs.screenName {
		user, err := twitter.GetUserByScreenName(ctx, client, screenName)
		if err != nil {
			log.Errorf("could not resolve the following list of %s, skipping: %v", screenName, err)
			continue
		}
		task.lists = append(task.lists, user.Following())
	}

	return &task, nil
}

type storePath struct {
	root   string
	users  string
	data   string
	db     string
	errorj string
}

// applyPathOverrides lets the environment fill in the two paths the
// configuration file may leave unspecified.
//
// The container image mounts /data and /state, and a mounted directory that the
// program ignores is worse than no mount at all: media written inside the
// container disappears with it, and a database written into the media volume
// defeats the point of separating them. A value in conf.yaml still wins, so an
// explicit configuration is never silently overridden -- but leaving the key out
// now means "use the mount", not "use /data/.data".
func applyPathOverrides(rootPath, statePath, confPath string) (string, string) {
	if rootPath == "" {
		if v := os.Getenv("TMD_ROOT_PATH"); v != "" {
			log.Infof("root_path is not set in %s; using TMD_ROOT_PATH=%s", confPath, v)
			rootPath = v
		}
	}
	if statePath == "" {
		if v := os.Getenv("TMD_STATE_PATH"); v != "" {
			log.Infof("state_path is not set in %s; using TMD_STATE_PATH=%s", confPath, v)
			statePath = v
		}
	}
	return rootPath, statePath
}

// newStorePath lays out the media root and the state directory and verifies
// that both can actually be written to. Failing here, with the offending path
// and the current UID, is far easier to act on than a permission error surfacing
// later as "failed to save failed tweet queue".
func newStorePath(root, stateDir string) (*storePath, error) {
	if root == "" {
		return nil, errors.New("root_path is empty; set it in conf.yaml")
	}
	if stateDir == "" {
		stateDir = filepath.Join(root, ".data")
	}

	ph := storePath{}
	ph.root = root
	ph.users = filepath.Join(root, "users")
	ph.data = stateDir
	ph.db = filepath.Join(ph.data, "foo.db")
	ph.errorj = filepath.Join(ph.data, "errors.json")

	for _, dir := range []string{ph.root, ph.users, ph.data} {
		// MkdirAll, not Mkdir: a parent that already exists is not an error,
		// and nested paths are legitimate.
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w%s", dir, err, writableHint(dir))
		}
		if err := checkWritable(dir); err != nil {
			return nil, err
		}
	}
	return &ph, nil
}

// writableHint explains, for a failed directory creation, what to check.
func writableHint(dir string) string {
	return fmt.Sprintf(" (running as uid=%d; a bind-mounted directory must already exist and be writable by that uid, or set user: in docker compose)", os.Getuid())
}

// checkWritable proves the directory accepts a file, which is what the run
// actually needs. A directory can exist and still be read-only, and the mount
// may be full.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".tmd-write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w%s", dir, err, writableHint(dir))
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func initLogger(dbg bool, logFile io.Writer) {
	// ForceColors writes ANSI escapes into the log file as well as the terminal,
	// which turns every container or cron log into escape soup. Respect the
	// conventional NO_COLOR (and TERM=dumb) so unattended runs produce a plain,
	// greppable log.
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"

	formatter := &log.TextFormatter{
		ForceColors:   !noColor,
		FullTimestamp: true,
	}
	if noColor {
		// The file hook needs an explicit non-colour formatter: lfshook follows
		// the logger's formatter otherwise, and ForceColors=false is not enough
		// to keep colour out of a redirected stream.
		formatter.DisableColors = true
	}
	log.SetFormatter(formatter)

	if dbg {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	if noColor {
		log.AddHook(lfshook.NewHook(logFile, &log.TextFormatter{
			FullTimestamp: true,
			DisableColors: true,
		}))
		return
	}
	log.AddHook(lfshook.NewHook(logFile, nil))
}

func run() int {
	//flags
	var usrArgs userArgs
	var listArgs ListArgs
	var follArgs userArgs
	var confArg bool
	var dbg bool
	var autoFollow bool
	var noRetry bool

	flag.BoolVar(&confArg, "conf", false, "reconfigure")
	flag.Var(&usrArgs, "user", "download tweets from the user specified by user_id/screen_name since the last download")
	flag.Var(&listArgs, "list", "batch download each member from list specified by list_id")
	flag.Var(&follArgs, "foll", "batch download each member followed by the user specified by user_id/screen_name")
	flag.BoolVar(&dbg, "dbg", false, "display debug message")
	flag.BoolVar(&autoFollow, "auto-follow", false, "send follow request automatically to protected users")
	flag.BoolVar(&noRetry, "no-retry", false, "quickly exit without retrying failed tweets")
	flag.StringVar(&targetsPath, "targets", "", "read the list of users to crawl from this file (default: <config dir>/targets.yaml)")
	flag.Parse()

	var err error

	// context
	ctx, cancel := context.WithCancel(context.Background())
	// Releasing it on every exit path keeps go vet's lostcancel check quiet and
	// lets the defers below run instead of being skipped by a bare return.
	defer cancel()

	var homepath string
	if runtime.GOOS == "windows" {
		homepath = os.Getenv("appdata")
	} else {
		homepath = os.Getenv("HOME")
	}
	if homepath == "" {
		panic("failed to get home path from env")
	}

	// The configuration directory may be redirected so that a container can
	// keep conf.yaml and the cookies on a mounted volume. Defaults to the
	// historical location.
	appRootPath := os.Getenv("TMD_CONFIG_DIR")
	if appRootPath == "" {
		appRootPath = filepath.Join(homepath, ".tmd2")
	}
	if abs, err := filepath.Abs(appRootPath); err == nil {
		appRootPath = abs
	}
	configDir = appRootPath
	if targetsPath == "" {
		targetsPath = configPath("targets.yaml")
	}
	confPath := filepath.Join(appRootPath, "conf.yaml")
	cliLogPath := filepath.Join(appRootPath, "client.log")
	logPath := filepath.Join(appRootPath, "tmd2.log")
	additionalCookiesPath := filepath.Join(appRootPath, "additional_cookies.yaml")
	if err = os.MkdirAll(appRootPath, 0755); err != nil {
		log.Fatalln("failed to make app dir", err)
	}

	// init logger
	logFile, err := os.OpenFile(logPath, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalln("failed to create log file:", err)
	}
	initLogger(dbg, logFile)

	// The target report is part of the run's contract: it is written no matter
	// how the run ends, so a skipped account is never silently invisible. It is
	// registered after the logger but closes the log file itself, which makes
	// the ordering explicit instead of depending on where this defer sits
	// relative to logFile.Close().
	defer func() {
		writeTargetReport()
		_ = logFile.Close()
	}()

	// report at exit
	defer func() {
		if dbg {
			twitter.ReportRequestCount()
		}
	}()

	// read/write config
	conf, err := readConf(confPath)
	if os.IsNotExist(err) || confArg {
		conf, err = promptConfig(confPath)
		if err != nil {
			log.Fatalln("config failure with", err)
		}
	}
	if err != nil {
		log.Fatalln("failed to load config:", err)
	}
	if confArg {
		log.Println("config done")
		return report.ExitOK
	}
	log.Infoln("config is loaded")

	// Environment overrides, applied only where the configuration file left the
	// choice open. In a container the mounts are the contract: a container that
	// mounted /data but wrote the media inside itself would lose everything on
	// exit, and a mounted /state that the database ignores is a wasted mount.
	conf.RootPath, conf.StatePath = applyPathOverrides(conf.RootPath, conf.StatePath, confPath)

	if conf.MaxDownloadRoutine > 0 {
		downloading.MaxDownloadRoutine = conf.MaxDownloadRoutine
	}

	// ensure store path exist
	pathHelper, err := newStorePath(conf.RootPath, conf.StatePath)
	if err != nil {
		log.Fatalln("failed to make store dir:", err)
	}

	// sign in
	client, screenName, err := twitter.Login(ctx, conf.Cookie.AuthCoken, conf.Cookie.Ct0)
	if err != nil {
		log.Fatalln("failed to login:", err)
	}
	twitter.EnableRateLimit(client)
	if dbg {
		twitter.EnableRequestCounting(client)
	}
	log.Infoln("signed in as:", color.FgLightBlue.Render(screenName))

	// load additional cookies
	cookies, err := readAdditionalCookies(additionalCookiesPath)
	if err != nil {
		log.Warnln("failed to load additional cookies:", err)
	}
	log.Debugln("loaded additional cookies:", len(cookies))
	addtional := batchLogin(ctx, dbg, cookies, screenName)

	// set clients logger
	cliLogFile, err := os.OpenFile(cliLogPath, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalln("failed to create log file:", err)
	}
	defer cliLogFile.Close()
	setClientLogger(client, cliLogFile)
	for _, cli := range addtional {
		setClientLogger(cli, cliLogFile)
	}

	// load previous tweets
	dumper := downloading.NewDumper()
	if err = loadRetryQueue(dumper, pathHelper.errorj); err != nil {
		log.Fatalln("failed to load previous tweets", err)
	}
	log.Infoln("loaded previous failed tweets:", dumper.Count())

	// collect tasks
	task, err := MakeTask(ctx, client, usrArgs, listArgs, follArgs)
	if err != nil {
		log.Fatalln("failed to parse cmd args:", err)
	}

	// connect db
	db, err := connectDatabase(pathHelper.db)
	if err != nil {
		log.Fatalln("failed to connect to database:", err)
	}
	defer db.Close()
	log.Infoln("database is connected")

	// listen signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer close(sigChan)
	defer signal.Stop(sigChan)
	go func() {
		sig, ok := <-sigChan
		if ok {
			log.Warnln("[listener] caught signal:", sig)
			cancel()
		}
	}()

	// Persist this run's failed tweets and only then advance the timeline
	// watermarks. Runs last (LIFO: after the retry defer below) so that the
	// retry pass has already updated the dumper.
	//
	// Cancellation must never skip this: the watermarks are advanced below, so
	// a queue that was not written would leave those tweets unreachable
	// forever. Skipping the retry *attempt* is fine; skipping the *save* is not.
	var todump = make([]*downloading.TweetInEntity, 0)
	defer func() {
		log.Infof("saving %d failed tweets to the retry queue", dumper.Count())
		if err := dumper.Dump(pathHelper.errorj); err != nil {
			// Do not advance the watermarks past tweets we failed to persist.
			log.Errorln("failed to save failed tweet queue, keeping timeline watermarks:", err)
			return
		}
		if err := downloading.CommitPersistedRetryProgress(db, todump); err != nil {
			log.Errorln("retry queue was saved, but failed to advance persisted timeline watermarks:", err)
		}
	}()

	// Re-queue this run's failed tweets, then opportunistically retry them.
	defer func() {
		for _, te := range todump {
			dumper.Push(te.Entity.Id(), te.Tweet)
		}

		// A manual cancellation skips the retry *attempt* only. The tweets were
		// pushed above and are still dumped by the deferred save below.
		if ctx.Err() != nil || noRetry {
			if dumper.Count() != 0 {
				log.Infof("%d failed tweets were queued and will be retried on the next run", dumper.Count())
			}
			return
		}
		if err := retryFailedTweets(ctx, dumper, db, client); err != nil {
			log.Errorln("failed to retry previously failed tweets:", err)
		}
	}()

	// do job
	if len(task.users) == 0 && len(task.lists) == 0 {
		log.Warnln("nothing to do: no usable targets")
		printTargetSummary()
		// Let the report decide: every target failing with an auth error is an
		// expired cookie (exit 3), other failures exit 1. Only a list that named
		// nothing usable at all is a usage problem.
		if targetReport.Len() != 0 {
			return targetReport.ExitCode()
		}
		if len(targetOrder) == 0 {
			return report.ExitOK // nothing was asked for
		}
		return report.ExitUsage
	}
	log.Infof("start working for: %d user(s), %d list(s)", len(task.users), len(task.lists))

	todump, stats, err := downloading.BatchDownloadAny(ctx, client, db, task.lists, task.users, pathHelper.root, pathHelper.users, autoFollow, addtional)
	if err != nil {
		log.Errorln("failed to download:", err)
	}

	// Map the per-entity download counts back onto the targets that were asked
	// for, so the report shows how much each account actually produced instead
	// of a placeholder.
	downloadedByTarget := make(map[string]int, len(task.users))
	for _, u := range task.users {
		label, known := targetLabelByUserID[u.Id]
		eid, hasEntity := downloading.UserEntityIDs[u.Id]
		if known && hasEntity {
			downloadedByTarget[label] = stats[eid]
		}
	}

	// One outcome per target, so a skipped account can never look like a
	// crawled one.
	failedByUser := make(map[string]int, len(todump))
	for _, item := range todump {
		name := item.Entity.Name()
		failedByUser[name]++
	}
	for _, t := range targetOrder {
		if targetReport.Has(t.Label()) {
			continue // already recorded as unresolved or skipped
		}
		if failed := failedByUser[t.Label()]; failed != 0 {
			targetReport.Failed(t.Label(), t.Value, targets.ReasonUnknown,
				fmt.Errorf("%d tweet(s) failed to download and were queued for retry", failed))
			continue
		}
		targetReport.OK(t.Label(), t.Value, downloadedByTarget[t.Label()])
	}

	return targetReport.ExitCode()
}

// printTargetSummary prints the requested targets, so the console shows the same
// picture as the report file.
func printTargetSummary() {
	if len(targetOrder) == 0 {
		return
	}
	fmt.Printf("targets: %d requested\n", len(targetOrder))
	for _, t := range targetOrder {
		fmt.Printf("    - %s\n", t.Label())
	}
}

// writeTargetReport persists the per-account outcomes and prints a summary. A
// failure to write is loud but not fatal: the exit code still reflects whether
// the crawl itself was complete.
func writeTargetReport() {
	if targetReport.Len() == 0 {
		return
	}

	path, err := targetReport.Write(configDir)
	if err != nil {
		log.Errorln("failed to write the target report:", err)
	}

	ok, skipped, failed := targetReport.Counts()
	log.Infof("targets: %d ok, %d skipped, %d failed", ok, skipped, failed)
	if path != "" {
		log.Infoln("target report:", path)
	}
	if accounts := targetReport.FailedAccounts(); len(accounts) != 0 {
		log.Warnf("could not crawl %d account(s): %s", len(accounts), strings.Join(accounts, ", "))
	}
}

func main() {
	os.Exit(run())
}

// loadRetryQueue loads the persisted retry queue. A queue damaged by an earlier
// interrupted or out-of-space write is moved aside instead of being fatal: a
// manual fix would otherwise be the only way to start the program again, and the
// user would lose the queue regardless.
func loadRetryQueue(dumper *downloading.TweetDumper, path string) error {
	err := dumper.Load(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}

	broken := path + ".corrupt"
	if renameErr := os.Rename(path, broken); renameErr != nil {
		return fmt.Errorf("unreadable retry queue (%w) and failed to move it aside: %v", err, renameErr)
	}
	log.WithError(err).Warnln("retry queue was unreadable and has been moved to", broken, "- starting with an empty queue")
	return nil
}

func setClientLogger(client *resty.Client, out io.Writer) {
	logger := log.New()
	logger.SetLevel(log.InfoLevel)
	logger.SetOutput(out)
	logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
		DisableQuote:  true,
	})
	client.SetLogger(logger)
}

func connectDatabase(path string) (*sqlx.DB, error) {
	ex, err := utils.PathExists(path)
	if err != nil {
		return nil, err
	}

	// The path is parsed as a URI when it is prefixed with "file:", so a path
	// containing '?' or '#' would silently open a different database. Escape it
	// and pass the options as a query.
	//
	// journal_mode=DELETE, not WAL: this is a single-writer program, so WAL
	// buys little, while it requires the -wal/-shm files to share a filesystem
	// and does not work on network shares at all. DELETE journals work
	// everywhere.
	//
	// busy_timeout is bounded: the previous value was effectively infinite,
	// which turned any lock problem into a silent hang with no error anywhere.
	dsn := fmt.Sprintf("file:%s?_journal_mode=DELETE&busy_timeout=30000&_foreign_keys=on",
		url.PathEscape(path))
	db, err := sqlx.Connect("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if err := database.CreateTables(db); err != nil {
		// Ignoring this left a schema problem to surface later as unrelated
		// write failures on a database that was never usable.
		_ = db.Close()
		return nil, fmt.Errorf("failed to prepare the database schema: %w", err)
	}
	if !ex {
		log.Debugln("created new db file", path)
	}
	return db, nil
}

func readConf(path string) (*Config, error) {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var result Config
	err = yaml.Unmarshal(data, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func writeConf(path string, conf *Config) error {
	file, err := os.OpenFile(path, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer file.Close()

	data, err := yaml.Marshal(conf)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, bytes.NewReader(data))
	return err
}

func promptConfig(saveto string) (*Config, error) {
	conf := Config{}
	scan := bufio.NewScanner(os.Stdin)

	print("enter storage dir: ")
	scan.Scan()
	storePath := scan.Text()
	// 确保路径可用
	err := os.MkdirAll(storePath, 0755)
	if err != nil {
		return nil, err
	}
	storePath, err = filepath.Abs(storePath)
	if err != nil {
		return nil, err
	}

	conf.RootPath = storePath

	print("enter auth_token: ")
	scan.Scan()
	conf.Cookie.AuthCoken = scan.Text()

	print("enter ct0: ")
	scan.Scan()
	conf.Cookie.Ct0 = scan.Text()

	print("enter max download routine: ")
	scan.Scan()
	conf.MaxDownloadRoutine, err = strconv.Atoi(scan.Text())
	if err != nil {
		return nil, err
	}

	return &conf, writeConf(saveto, &conf)
}

func retryFailedTweets(ctx context.Context, dumper *downloading.TweetDumper, db *sqlx.DB, client *resty.Client) error {
	if dumper.Count() == 0 {
		return nil
	}

	log.Infoln("starting to retry failed tweets")
	legacy, err := dumper.GetTotal(db)
	if err != nil {
		return err
	}

	toretry := make([]downloading.PackgedTweet, 0, len(legacy))
	for _, leg := range legacy {
		toretry = append(toretry, leg)
	}

	newFails := downloading.BatchDownloadTweet(ctx, client, toretry...)
	attempted := make(map[int][]*twitter.Tweet)
	for _, leg := range legacy {
		attempted[leg.Entity.Id()] = append(attempted[leg.Entity.Id()], leg.Tweet)
	}
	for entityID, tweets := range attempted {
		dumper.Remove(entityID, tweets...)
	}
	for _, pt := range newFails {
		te := pt.(*downloading.TweetInEntity)
		dumper.Push(te.Entity.Id(), te.Tweet)
	}

	return nil
}

func readAdditionalCookies(path string) ([]*Cookie, error) {
	res := []*Cookie{}
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	return res, yaml.Unmarshal(data, &res)
}

func batchLogin(ctx context.Context, dbg bool, cookies []*Cookie, master string) []*resty.Client {
	if len(cookies) == 0 {
		return nil
	}

	added := sync.Map{}
	msgs := make([]string, len(cookies))
	clients := []*resty.Client{}
	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}
	added.Store(master, struct{}{})

	for i, cookie := range cookies {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			cli, sn, err := twitter.Login(ctx, cookie.AuthCoken, cookie.Ct0)
			if _, loaded := added.LoadOrStore(sn, struct{}{}); loaded {
				msgs[index] = "    - ? repeated\n"
				return
			}

			if err != nil {
				msgs[index] = fmt.Sprintf("    - ? %v\n", err)
				return
			}
			twitter.EnableRateLimit(cli)
			if dbg {
				twitter.EnableRequestCounting(cli)
			}
			mtx.Lock()
			defer mtx.Unlock()
			clients = append(clients, cli)
			msgs[index] = fmt.Sprintf("    - %s\n", sn)
		}(i)
	}

	wg.Wait()
	log.Infoln("loaded additional accounts:", len(clients))
	for _, msg := range msgs {
		fmt.Print(msg)
	}
	return clients
}

package main

import (
	"./sessions"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/gorilla/securecookie"
	"github.com/knieriem/markdown"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	memosPerPage   = 100
	listenAddr     = ":5000"
	sessionName    = "isucon_session"
	tmpDir         = "/tmp/"
	dbConnPoolSize = 256
	sessionFile    = "/dev/shm/gorilla"
	sessionSecret  = "kH<{11qpic*gf0e21YK7YtwyUvE9l<1r>yX8R-Op"
)

type Config struct {
	Database struct {
		Dbname   string `json:"dbname"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"database"`
}

type User struct {
	Id         int
	Username   string
	Password   string
	Salt       string
	LastAccess string
}

type Memo struct {
	Id        int
	User      int
	Content   string
	IsPrivate int
	CreatedAt string
	UpdatedAt string
	Username  string
}

type Memos []*Memo

type View struct {
	User      *User
	Memo      *Memo
	Memos     *Memos
	Page      int
	PageStart int
	PageEnd   int
	Total     int
	Older     *Memo
	Newer     *Memo
	Session   *sessions.Session
}

var M = struct {
	lock      sync.RWMutex
	memoCount int
	memos     map[int]*Memo
}{
	lock:      sync.RWMutex{},
	memoCount: 0,
	memos:     make(map[int]*Memo),
}

var (
	users   = make(map[int]*User)
	conn    *sql.DB
	baseUrl *url.URL
	fmap    = template.FuncMap{
		"url_for": func(path string) string {
			return baseUrl.String() + path
		},
		"first_line": func(s string) string {
			sl := strings.Split(s, "\n")
			return sl[0]
		},
		"get_token": func(session *sessions.Session) interface{} {
			return session.Values["token"]
		},
		"gen_markdown": func(s string) template.HTML {
			var buf bytes.Buffer
			p := markdown.NewParser(nil)
			p.Markdown(bytes.NewBufferString(s), markdown.ToHTML(&buf))

			return template.HTML(buf.String())
		},
	}
	tmpl = template.Must(template.New("tmpl").Funcs(fmap).ParseGlob("templates/*.html"))
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	env := os.Getenv("ISUCON_ENV")
	if env == "" {
		env = "local"
	}
	config := loadConfig("../config/" + env + ".json")
	db := config.Database
	connectionString := fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=utf8",
		db.Username, db.Password, db.Host, db.Port, db.Dbname,
	)
	log.Printf("db: %s", connectionString)

	var err error
	conn, err = sql.Open("mysql", connectionString)
	if err != nil {
		log.Panic(err)
	}
	conn.SetMaxIdleConns(dbConnPoolSize)

	initialize()

	r := mux.NewRouter()

	r.HandleFunc("/", topHandler)
	r.HandleFunc("/signin", signinHandler).Methods("GET", "HEAD")
	r.HandleFunc("/signin", signinPostHandler).Methods("POST")
	r.HandleFunc("/signout", signoutHandler)
	r.HandleFunc("/mypage", mypageHandler)
	r.HandleFunc("/memo/{memo_id}", memoHandler).Methods("GET", "HEAD")
	r.HandleFunc("/memo", memoPostHandler).Methods("POST")
	r.HandleFunc("/recent/{page:[0-9]+}", recentHandler)
	r.HandleFunc("/reset", resetHandler)
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./public/")))
	http.Handle("/", r)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func loadConfig(filename string) *Config {
	log.Printf("loading config file: %s", filename)
	f, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
	var config Config
	err = json.Unmarshal(f, &config)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
	return &config
}

func prepareHandler(w http.ResponseWriter, r *http.Request) {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		baseUrl, _ = url.Parse("http://" + h)
	} else {
		baseUrl, _ = url.Parse("http://" + r.Host)
	}
}

func loadSession(w http.ResponseWriter, r *http.Request) (session *sessions.Session, err error) {
	store := sessions.NewFilesystemStore(sessionFile, []byte(sessionSecret))
	return store.Get(r, sessionName)
}

func getUser(w http.ResponseWriter, r *http.Request, session *sessions.Session) *User {
	userId := session.Values["user_id"]
	if userId == nil {
		return nil
	}
	user, ok := users[userId.(int)]
	if ok {
		w.Header().Add("Cache-Control", "private")
	}
	return user
}

func antiCSRF(w http.ResponseWriter, r *http.Request, session *sessions.Session) bool {
	if r.FormValue("sid") != session.Values["token"] {
		code := http.StatusBadRequest
		http.Error(w, http.StatusText(code), code)
		return true
	}
	return false
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("error: %s", err)
	code := http.StatusInternalServerError
	http.Error(w, http.StatusText(code), code)
}

func notFound(w http.ResponseWriter) {
	code := http.StatusNotFound
	http.Error(w, http.StatusText(code), code)
}

func topHandler(w http.ResponseWriter, r *http.Request) {
	M.lock.Lock()
	defer M.lock.Unlock()

	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	user := getUser(w, r, session)

	rows, err := conn.Query("SELECT * FROM memos WHERE is_private=0 ORDER BY created_at DESC, id DESC LIMIT ?", memosPerPage)
	if err != nil {
		serverError(w, err)
		return
	}
	memos := make(Memos, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	for rows.Next() {
		memo := Memo{}
		rows.Scan(&memo.Id, &memo.User, &memo.Content, &memo.IsPrivate, &memo.CreatedAt, &memo.UpdatedAt)
		memo.Username = users[memo.User].Username
		memos = append(memos, &memo)
	}
	rows.Close()

	v := &View{
		Total:     M.memoCount,
		Page:      0,
		PageStart: 1,
		PageEnd:   memosPerPage,
		Memos:     &memos,
		User:      user,
		Session:   session,
	}
	if err = tmpl.ExecuteTemplate(w, "index", v); err != nil {
		serverError(w, err)
	}
}

func recentHandler(w http.ResponseWriter, r *http.Request) {
	M.lock.Lock()
	defer M.lock.Unlock()

	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	user := getUser(w, r, session)
	vars := mux.Vars(r)
	page, _ := strconv.Atoi(vars["page"])

	rows, err := conn.Query("SELECT * FROM memos WHERE is_private=0 ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?", memosPerPage, memosPerPage*page)
	if err != nil {
		serverError(w, err)
		return
	}
	memos := make(Memos, 0)
	for rows.Next() {
		memo := Memo{}
		rows.Scan(&memo.Id, &memo.User, &memo.Content, &memo.IsPrivate, &memo.CreatedAt, &memo.UpdatedAt)
		memo.Username = users[memo.User].Username
		memos = append(memos, &memo)
	}
	if len(memos) == 0 {
		notFound(w)
		return
	}

	v := &View{
		Total:     M.memoCount,
		Page:      page,
		PageStart: memosPerPage*page + 1,
		PageEnd:   memosPerPage * (page + 1),
		Memos:     &memos,
		User:      user,
		Session:   session,
	}
	if err = tmpl.ExecuteTemplate(w, "index", v); err != nil {
		serverError(w, err)
	}
}

func signinHandler(w http.ResponseWriter, r *http.Request) {
	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	user := getUser(w, r, session)

	v := &View{
		User:    user,
		Session: session,
	}
	if err := tmpl.ExecuteTemplate(w, "signin", v); err != nil {
		serverError(w, err)
		return
	}
}

func signinPostHandler(w http.ResponseWriter, r *http.Request) {
	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)

	username := r.FormValue("username")
	password := r.FormValue("password")
	var user *User
	for _, u := range users {
		if u.Username == username {
			user = u
			break
		}
	}
	if user != nil {
		h := sha256.New()
		h.Write([]byte(user.Salt + password))
		if user.Password == fmt.Sprintf("%x", h.Sum(nil)) {
			session.Values["user_id"] = user.Id
			session.Values["token"] = fmt.Sprintf("%x", securecookie.GenerateRandomKey(32))
			if err := session.Save(r, w); err != nil {
				serverError(w, err)
				return
			}
			if _, err := conn.Exec("UPDATE users SET last_access=now() WHERE id=?", user.Id); err != nil {
				serverError(w, err)
				return
			} else {
				http.Redirect(w, r, "/mypage", http.StatusFound)
			}
			return
		}
	}
	v := &View{
		Session: session,
	}
	if err := tmpl.ExecuteTemplate(w, "signin", v); err != nil {
		serverError(w, err)
		return
	}
}

func signoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	if antiCSRF(w, r, session) {
		return
	}

	http.SetCookie(w, sessions.NewCookie(sessionName, "", &sessions.Options{MaxAge: -1}))
	http.Redirect(w, r, "/", http.StatusFound)
}

func mypageHandler(w http.ResponseWriter, r *http.Request) {
	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)

	user := getUser(w, r, session)
	if user == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	rows, err := conn.Query("SELECT id, content, is_private, created_at, updated_at FROM memos WHERE user=? ORDER BY created_at DESC", user.Id)
	if err != nil {
		serverError(w, err)
		return
	}
	memos := make(Memos, 0)
	for rows.Next() {
		memo := Memo{}
		rows.Scan(&memo.Id, &memo.Content, &memo.IsPrivate, &memo.CreatedAt, &memo.UpdatedAt)
		memos = append(memos, &memo)
	}
	v := &View{
		Memos:   &memos,
		User:    user,
		Session: session,
	}
	if err = tmpl.ExecuteTemplate(w, "mypage", v); err != nil {
		serverError(w, err)
	}
}

func memoHandler(w http.ResponseWriter, r *http.Request) {
	M.lock.Lock()
	defer M.lock.Unlock()

	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	vars := mux.Vars(r)
	memoId := vars["memo_id"]
	user := getUser(w, r, session)

	memoIdInt, _ := strconv.Atoi(memoId)
	memo, found := M.memos[memoIdInt]
	if !found {
		notFound(w)
		return
	}
	if memo.IsPrivate == 1 {
		if user == nil || user.Id != memo.User {
			notFound(w)
			return
		}
	}

	var cond string
	if user != nil && user.Id == memo.User {
		cond = ""
	} else {
		cond = "AND is_private=0"
	}
	rows, err := conn.Query("SELECT id, content, is_private, created_at, updated_at FROM memos WHERE user=? "+cond+" ORDER BY created_at", memo.User)
	if err != nil {
		serverError(w, err)
		return
	}
	memos := make(Memos, 0)
	for rows.Next() {
		m := Memo{}
		rows.Scan(&m.Id, &m.Content, &m.IsPrivate, &m.CreatedAt, &m.UpdatedAt)
		memos = append(memos, &m)
	}
	rows.Close()
	var older *Memo
	var newer *Memo
	for i, m := range memos {
		if m.Id == memo.Id {
			if i > 0 {
				older = memos[i-1]
			}
			if i < len(memos)-1 {
				newer = memos[i+1]
			}
		}
	}

	v := &View{
		User:    user,
		Memo:    memo,
		Older:   older,
		Newer:   newer,
		Session: session,
	}
	if err = tmpl.ExecuteTemplate(w, "memo", v); err != nil {
		serverError(w, err)
	}
}

func memoPostHandler(w http.ResponseWriter, r *http.Request) {
	session, err := loadSession(w, r)
	if err != nil {
		serverError(w, err)
		return
	}
	prepareHandler(w, r)
	if antiCSRF(w, r, session) {
		return
	}

	user := getUser(w, r, session)
	if user == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	var isPrivate int
	if r.FormValue("is_private") == "1" {
		isPrivate = 1
	} else {
		isPrivate = 0
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	result, err := conn.Exec(
		"INSERT INTO memos (user, content, is_private, created_at) VALUES (?, ?, ?, ?)",
		user.Id, r.FormValue("content"), isPrivate, fmt.Sprintf("%s", now),
	)
	if err != nil {
		serverError(w, err)
		return
	}
	newId, _ := result.LastInsertId()

	M.lock.Lock()
	memo := &Memo{
		Id:        int(newId),
		User:      user.Id,
		Content:   r.FormValue("content"),
		IsPrivate: isPrivate,
		CreatedAt: now,
		UpdatedAt: now,
	}
	addMemo(memo)
	M.lock.Unlock()

	http.Redirect(w, r, fmt.Sprintf("/memo/%d", newId), http.StatusFound)
}

func addMemo(memo *Memo) {
	if _, found := M.memos[memo.Id]; !found {
		M.memos[memo.Id] = memo
		if memo.IsPrivate == 0 {
			M.memoCount++
		}
	}
}

func initialize() {
	M.lock.Lock()
	defer M.lock.Unlock()

	rows, _ := conn.Query("SELECT * FROM users")
	for rows.Next() {
		user := &User{}
		rows.Scan(&user.Id, &user.Username, &user.Password, &user.Salt, &user.LastAccess)
		users[user.Id] = user
	}
	rows.Close()

	M.memoCount = 0
	M.memos = make(map[int]*Memo)
	rows, _ = conn.Query("SELECT id, user, content, is_private, created_at, updated_at FROM memos")
	for rows.Next() {
		var memo Memo
		rows.Scan(&memo.Id, &memo.User, &memo.Content, &memo.IsPrivate, &memo.CreatedAt, &memo.UpdatedAt)
		memo.Username = users[memo.User].Username
		addMemo(&memo)
	}
	rows.Close()
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	initialize()
	w.Write([]byte("OK"))
}

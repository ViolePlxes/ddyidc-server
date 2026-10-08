package update

import "time"

const (
	TargetBackend  = "backend"
	TargetFrontend = "frontend"

	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

type Task struct {
	ID             int64      `gorm:"column:id;primaryKey" json:"-"`
	Target         string     `gorm:"column:target" json:"target"`
	Status         string     `gorm:"column:status" json:"status"`
	Progress       int        `gorm:"column:progress" json:"progress"`
	TriggerBy      int64      `gorm:"column:trigger_by" json:"-"`
	Branch         string     `gorm:"column:branch" json:"branch"`
	Feature        string     `gorm:"column:feature" json:"feature"`
	BeforeCommit   string     `gorm:"column:before_commit" json:"beforeCommit"`
	TargetCommit   string     `gorm:"column:target_commit" json:"targetCommit"`
	BeforeVersion  string     `gorm:"column:before_version" json:"beforeVersion"`
	TargetVersion  string     `gorm:"column:target_version" json:"targetVersion"`
	Log            string     `gorm:"column:log" json:"log"`
	Error          string     `gorm:"column:error" json:"error,omitempty"`
	RollbackStatus string     `gorm:"column:rollback_status" json:"rollbackStatus,omitempty"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"createdAt"`
	StartedAt      time.Time  `gorm:"column:started_at" json:"startedAt"`
	FinishedAt     *time.Time `gorm:"column:finished_at" json:"finishedAt,omitempty"`
}

func (Task) TableName() string { return "system.update_tasks" }

type TaskView struct {
	ID string `json:"id"`
	Task
}

func viewTask(task *Task) TaskView {
	return TaskView{ID: formatID(task.ID), Task: *task}
}

type Change struct {
	Commit  string `json:"commit"`
	Feature string `json:"feature"`
	Time    string `json:"time"`
}

type VersionInfo struct {
	Target        string   `json:"target"`
	Current       string   `json:"currentVersion"`
	Remote        string   `json:"remoteVersion"`
	CurrentCommit string   `json:"currentCommit"`
	RemoteCommit  string   `json:"remoteCommit"`
	Branch        string   `json:"branch"`
	UpdatedAt     string   `json:"updatedAt"`
	HasUpdate     bool     `json:"hasUpdate"`
	Ahead         int      `json:"ahead"`
	Behind        int      `json:"behind"`
	Changes       []Change `json:"changes"`
}

type StatusView struct {
	Backend  VersionInfo `json:"backend"`
	Frontend VersionInfo `json:"frontend"`
	Running  *TaskView   `json:"runningTask,omitempty"`
}

type CheckResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type HelperResult struct {
	TaskID         string    `json:"taskId"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	RollbackStatus string    `json:"rollbackStatus,omitempty"`
	Log            string    `json:"log"`
	FinishedAt     time.Time `json:"finishedAt"`
}

type HelperPlan struct {
	TaskID            string `json:"taskId"`
	ParentPID         int    `json:"parentPid"`
	ParentIdentity    string `json:"parentIdentity,omitempty"`
	NewExecutable     string `json:"newExecutable"`
	CurrentExecutable string `json:"currentExecutable"`
	BackupExecutable  string `json:"backupExecutable"`
	Workspace         string `json:"workspace"`
	BeforeCommit      string `json:"beforeCommit"`
	GitPath           string `json:"gitPath"`
	ServerDirectory   string `json:"serverDirectory"`
	ConfigFile        string `json:"configFile"`
	HealthURL         string `json:"healthUrl"`
	VersionURL        string `json:"versionUrl"`
	SmokeURL          string `json:"smokeUrl"`
	ExpectedCommit    string `json:"expectedCommit"`
	ResultFile        string `json:"resultFile"`
	TimeoutSeconds    int    `json:"timeoutSeconds"`
	MinFreeMB         uint64 `json:"minFreeMB"`
	SystemctlPath     string `json:"systemctlPath"`
	SudoPath          string `json:"sudoPath"`
	ServiceName       string `json:"serviceName"`
}

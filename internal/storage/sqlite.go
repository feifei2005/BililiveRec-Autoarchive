// Package storage 提供数据持久化功能
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// CoverHistory 封面历史记录
type CoverHistory struct {
	ID         int64     // 记录ID
	StreamerID string    // 主播ID
	CoverPath  string    // 封面路径
	RecordDate time.Time // 录制日期
	CreatedAt  time.Time // 创建时间
}

// ErrorEntry 错误日志条目
type ErrorEntry struct {
	ID        int64     // 记录ID
	Timestamp time.Time // 发生时间
	FilePath  string    // 相关文件路径
	Error     string    // 错误信息
	Resolved  bool      // 是否已解决
}

// ProcessLog 处理日志记录
type ProcessLog struct {
	ID         int64     // 记录ID
	TaskID     string    // 任务ID
	InputPath  string    // 输入文件路径
	OutputPath string    // 输出文件路径
	Status     string    // 处理状态
	Error      string    // 错误信息
	StartTime  time.Time // 开始时间
	EndTime    time.Time // 结束时间
	CreatedAt  time.Time // 创建时间
}

// Storage 存储接口
type Storage interface {
	// 封面历史相关
	SaveCoverHistory(ctx context.Context, history *CoverHistory) error
	GetLatestCover(ctx context.Context, streamerID string) (*CoverHistory, error)
	ListCoverHistory(ctx context.Context, streamerID string, limit int) ([]*CoverHistory, error)

	// 简化的封面存取接口
	SaveStreamerCover(streamerName, coverPath string) error
	GetLastStreamerCover(streamerName string) (string, error)

	// 处理日志相关
	SaveProcessLog(ctx context.Context, log *ProcessLog) error
	LogProcessResult(result ProcessLog) error
	GetProcessLog(ctx context.Context, taskID string) (*ProcessLog, error)
	ListProcessLogs(ctx context.Context, limit, offset int) ([]*ProcessLog, error)
	ListFailedLogs(ctx context.Context, limit int) ([]*ProcessLog, error)
	IsFileProcessed(inputPath string) bool

	// 生命周期
	Close() error
}

// Config 存储配置
type Config struct {
	DBPath string // 数据库文件路径
}

// SQLiteStorage SQLite 存储实现
type SQLiteStorage struct {
	config Config
	db     *sql.DB
}

// New 创建新的 SQLite 存储实例
func New(cfg Config) (*SQLiteStorage, error) {
	if cfg.DBPath == "" {
		cfg.DBPath = "data.db"
	}

	// 确保数据库目录存在
	dbDir := filepath.Dir(cfg.DBPath)
	if dbDir != "" && dbDir != "." {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	// 打开数据库连接
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 测试连接
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// 设置连接池参数
	db.SetMaxOpenConns(1) // SQLite 单连接性能更好
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	storage := &SQLiteStorage{
		config: cfg,
		db:     db,
	}

	// 执行数据库迁移
	if err := storage.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return storage, nil
}

// migrate 数据库迁移
func (s *SQLiteStorage) migrate() error {
	// 创建封面历史表
	createCoverHistoryTable := `
	CREATE TABLE IF NOT EXISTS cover_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		streamer_id TEXT NOT NULL,
		cover_path TEXT NOT NULL,
		record_date DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`

	// 创建主播封面索引（用于快速查找最新封面）
	createCoverIndex := `
	CREATE INDEX IF NOT EXISTS idx_cover_history_streamer 
	ON cover_history(streamer_id, created_at DESC);
	`

	// 创建处理日志表
	createProcessLogTable := `
	CREATE TABLE IF NOT EXISTS process_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT,
		input_path TEXT NOT NULL,
		output_path TEXT,
		status TEXT NOT NULL,
		error TEXT,
		start_time DATETIME,
		end_time DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`

	// 创建处理日志索引
	createLogIndex := `
	CREATE INDEX IF NOT EXISTS idx_process_logs_status 
	ON process_logs(status, created_at DESC);
	`

	createLogTaskIndex := `
	CREATE INDEX IF NOT EXISTS idx_process_logs_task_id 
	ON process_logs(task_id);
	`

	// 执行所有迁移语句
	migrations := []string{
		createCoverHistoryTable,
		createCoverIndex,
		createProcessLogTable,
		createLogIndex,
		createLogTaskIndex,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// SaveCoverHistory 保存封面历史
func (s *SQLiteStorage) SaveCoverHistory(ctx context.Context, history *CoverHistory) error {
	query := `
	INSERT INTO cover_history (streamer_id, cover_path, record_date, created_at)
	VALUES (?, ?, ?, ?)
	`

	result, err := s.db.ExecContext(ctx, query,
		history.StreamerID,
		history.CoverPath,
		history.RecordDate,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to save cover history: %w", err)
	}

	id, err := result.LastInsertId()
	if err == nil {
		history.ID = id
	}

	return nil
}

// SaveStreamerCover 保存主播封面（简化接口）
// 记录主播最后一次成功处理时使用的封面路径
func (s *SQLiteStorage) SaveStreamerCover(streamerName, coverPath string) error {
	// 检查封面文件是否存在
	if _, err := os.Stat(coverPath); err != nil {
		return fmt.Errorf("cover file does not exist: %w", err)
	}

	history := &CoverHistory{
		StreamerID: streamerName,
		CoverPath:  coverPath,
		RecordDate: time.Now(),
	}

	return s.SaveCoverHistory(context.Background(), history)
}

// GetLatestCover 获取主播最新封面
func (s *SQLiteStorage) GetLatestCover(ctx context.Context, streamerID string) (*CoverHistory, error) {
	query := `
	SELECT id, streamer_id, cover_path, record_date, created_at
	FROM cover_history
	WHERE streamer_id = ?
	ORDER BY created_at DESC
	LIMIT 1
	`

	row := s.db.QueryRowContext(ctx, query, streamerID)

	var history CoverHistory
	var recordDate, createdAt sql.NullTime

	err := row.Scan(
		&history.ID,
		&history.StreamerID,
		&history.CoverPath,
		&recordDate,
		&createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // 无历史记录
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get latest cover: %w", err)
	}

	if recordDate.Valid {
		history.RecordDate = recordDate.Time
	}
	if createdAt.Valid {
		history.CreatedAt = createdAt.Time
	}

	return &history, nil
}

// GetLastStreamerCover 获取主播的历史封面路径（简化接口）
// 返回该主播上一次成功处理使用的封面路径
// 如果封面文件不存在，会继续查找更早的记录
func (s *SQLiteStorage) GetLastStreamerCover(streamerName string) (string, error) {
	query := `
	SELECT cover_path
	FROM cover_history
	WHERE streamer_id = ?
	ORDER BY created_at DESC
	LIMIT 10
	`

	rows, err := s.db.Query(query, streamerName)
	if err != nil {
		return "", fmt.Errorf("failed to query cover history: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var coverPath string
		if err := rows.Scan(&coverPath); err != nil {
			continue
		}

		// 检查文件是否存在
		if _, err := os.Stat(coverPath); err == nil {
			return coverPath, nil
		}
	}

	// 无有效的历史封面
	return "", nil
}

// ListCoverHistory 列出封面历史
func (s *SQLiteStorage) ListCoverHistory(ctx context.Context, streamerID string, limit int) ([]*CoverHistory, error) {
	if limit <= 0 {
		limit = 10
	}

	query := `
	SELECT id, streamer_id, cover_path, record_date, created_at
	FROM cover_history
	WHERE streamer_id = ?
	ORDER BY created_at DESC
	LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, streamerID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list cover history: %w", err)
	}
	defer rows.Close()

	var histories []*CoverHistory
	for rows.Next() {
		var history CoverHistory
		var recordDate, createdAt sql.NullTime

		err := rows.Scan(
			&history.ID,
			&history.StreamerID,
			&history.CoverPath,
			&recordDate,
			&createdAt,
		)
		if err != nil {
			continue
		}

		if recordDate.Valid {
			history.RecordDate = recordDate.Time
		}
		if createdAt.Valid {
			history.CreatedAt = createdAt.Time
		}

		histories = append(histories, &history)
	}

	return histories, nil
}

// SaveProcessLog 保存处理日志
func (s *SQLiteStorage) SaveProcessLog(ctx context.Context, log *ProcessLog) error {
	query := `
	INSERT INTO process_logs (task_id, input_path, output_path, status, error, start_time, end_time, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := s.db.ExecContext(ctx, query,
		log.TaskID,
		log.InputPath,
		log.OutputPath,
		log.Status,
		log.Error,
		log.StartTime,
		log.EndTime,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to save process log: %w", err)
	}

	id, err := result.LastInsertId()
	if err == nil {
		log.ID = id
	}

	return nil
}

// LogProcessResult 记录处理结果（简化接口）
func (s *SQLiteStorage) LogProcessResult(result ProcessLog) error {
	return s.SaveProcessLog(context.Background(), &result)
}

// GetProcessLog 获取处理日志
func (s *SQLiteStorage) GetProcessLog(ctx context.Context, taskID string) (*ProcessLog, error) {
	query := `
	SELECT id, task_id, input_path, output_path, status, error, start_time, end_time, created_at
	FROM process_logs
	WHERE task_id = ?
	LIMIT 1
	`

	row := s.db.QueryRowContext(ctx, query, taskID)

	var log ProcessLog
	var startTime, endTime, createdAt sql.NullTime
	var outputPath, errStr sql.NullString

	err := row.Scan(
		&log.ID,
		&log.TaskID,
		&log.InputPath,
		&outputPath,
		&log.Status,
		&errStr,
		&startTime,
		&endTime,
		&createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get process log: %w", err)
	}

	if outputPath.Valid {
		log.OutputPath = outputPath.String
	}
	if errStr.Valid {
		log.Error = errStr.String
	}
	if startTime.Valid {
		log.StartTime = startTime.Time
	}
	if endTime.Valid {
		log.EndTime = endTime.Time
	}
	if createdAt.Valid {
		log.CreatedAt = createdAt.Time
	}

	return &log, nil
}

// ListProcessLogs 列出处理日志
func (s *SQLiteStorage) ListProcessLogs(ctx context.Context, limit, offset int) ([]*ProcessLog, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT id, task_id, input_path, output_path, status, error, start_time, end_time, created_at
	FROM process_logs
	ORDER BY created_at DESC
	LIMIT ? OFFSET ?
	`

	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list process logs: %w", err)
	}
	defer rows.Close()

	return s.scanProcessLogs(rows)
}

// ListFailedLogs 列出失败的处理日志
func (s *SQLiteStorage) ListFailedLogs(ctx context.Context, limit int) ([]*ProcessLog, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
	SELECT id, task_id, input_path, output_path, status, error, start_time, end_time, created_at
	FROM process_logs
	WHERE status = 'failed'
	ORDER BY created_at DESC
	LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list failed logs: %w", err)
	}
	defer rows.Close()

	return s.scanProcessLogs(rows)
}

// scanProcessLogs 扫描处理日志行
func (s *SQLiteStorage) scanProcessLogs(rows *sql.Rows) ([]*ProcessLog, error) {
	var logs []*ProcessLog

	for rows.Next() {
		var log ProcessLog
		var taskID, outputPath, errStr sql.NullString
		var startTime, endTime, createdAt sql.NullTime

		err := rows.Scan(
			&log.ID,
			&taskID,
			&log.InputPath,
			&outputPath,
			&log.Status,
			&errStr,
			&startTime,
			&endTime,
			&createdAt,
		)
		if err != nil {
			continue
		}

		if taskID.Valid {
			log.TaskID = taskID.String
		}
		if outputPath.Valid {
			log.OutputPath = outputPath.String
		}
		if errStr.Valid {
			log.Error = errStr.String
		}
		if startTime.Valid {
			log.StartTime = startTime.Time
		}
		if endTime.Valid {
			log.EndTime = endTime.Time
		}
		if createdAt.Valid {
			log.CreatedAt = createdAt.Time
		}

		logs = append(logs, &log)
	}

	return logs, nil
}

// IsFileProcessed 检查文件是否已成功处理过
// 通过查询 process_logs 表中是否存在该输入路径且状态为 completed 的记录
func (s *SQLiteStorage) IsFileProcessed(inputPath string) bool {
	query := `
	SELECT COUNT(*) FROM process_logs
	WHERE input_path = ? AND status = 'completed'
	`

	var count int
	err := s.db.QueryRow(query, inputPath).Scan(&count)
	if err != nil {
		// 查询出错时返回 false，让处理流程继续
		return false
	}

	return count > 0
}

// Close 关闭数据库连接
func (s *SQLiteStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

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
// 通过查询 process_logs 表中是否存在该输入路径且状态为 success 的记录
func (s *SQLiteStorage) IsFileProcessed(inputPath string) bool {
	query := `
	SELECT COUNT(*) FROM process_logs
	WHERE input_path = ? AND status = 'success'
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

// ================== 数据库清理功能 ==================

// CleanupConfig 清理配置
type CleanupConfig struct {
	// CompletedMaxDays 已完成任务保留天数（默认 7 天）
	CompletedMaxDays int
	// FailedMaxDays 失败任务保留天数（默认 30 天）
	FailedMaxDays int
	// OtherMaxDays 其他状态任务保留天数（默认 30 天）
	OtherMaxDays int
	// CleanupOrphaned 是否清理源文件已删除的记录
	CleanupOrphaned bool
	// CoverHistoryMaxDays 封面历史保留天数（默认 90 天）
	CoverHistoryMaxDays int
}

// DefaultCleanupConfig 返回默认清理配置
func DefaultCleanupConfig() CleanupConfig {
	return CleanupConfig{
		CompletedMaxDays:    7,
		FailedMaxDays:       30,
		OtherMaxDays:        30,
		CleanupOrphaned:     true,
		CoverHistoryMaxDays: 90,
	}
}

// CleanupResult 清理结果
type CleanupResult struct {
	DeletedCompleted int // 删除的已完成记录数
	DeletedFailed    int // 删除的失败记录数
	DeletedOther     int // 删除的其他状态记录数
	DeletedOrphaned  int // 删除的孤立记录数
	DeletedCovers    int // 删除的封面历史记录数
	Errors           []error
}

// CleanupOldRecords 清理旧的任务记录
// 根据配置清理超过指定天数的记录
func (s *SQLiteStorage) CleanupOldRecords(cfg CleanupConfig) CleanupResult {
	result := CleanupResult{}

	// 1. 清理已完成的任务记录
	if cfg.CompletedMaxDays > 0 {
		threshold := time.Now().AddDate(0, 0, -cfg.CompletedMaxDays)
		query := `DELETE FROM process_logs WHERE status = 'success' AND created_at < ?`
		res, err := s.db.Exec(query, threshold)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("清理已完成记录失败: %w", err))
		} else if affected, err := res.RowsAffected(); err == nil {
			result.DeletedCompleted = int(affected)
		}
	}

	// 2. 清理失败的任务记录
	if cfg.FailedMaxDays > 0 {
		threshold := time.Now().AddDate(0, 0, -cfg.FailedMaxDays)
		query := `DELETE FROM process_logs WHERE status = 'failed' AND created_at < ?`
		res, err := s.db.Exec(query, threshold)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("清理失败记录失败: %w", err))
		} else if affected, err := res.RowsAffected(); err == nil {
			result.DeletedFailed = int(affected)
		}
	}

	// 3. 清理其他状态的任务记录（如 discarded 等）
	if cfg.OtherMaxDays > 0 {
		threshold := time.Now().AddDate(0, 0, -cfg.OtherMaxDays)
		query := `DELETE FROM process_logs WHERE status NOT IN ('success', 'failed', 'pending', 'processing') AND created_at < ?`
		res, err := s.db.Exec(query, threshold)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("清理其他记录失败: %w", err))
		} else if affected, err := res.RowsAffected(); err == nil {
			result.DeletedOther = int(affected)
		}
	}

	// 4. 清理封面历史记录
	if cfg.CoverHistoryMaxDays > 0 {
		threshold := time.Now().AddDate(0, 0, -cfg.CoverHistoryMaxDays)
		query := `DELETE FROM cover_history WHERE created_at < ?`
		res, err := s.db.Exec(query, threshold)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("清理封面历史失败: %w", err))
		} else if affected, err := res.RowsAffected(); err == nil {
			result.DeletedCovers = int(affected)
		}
	}

	// 5. 清理���文件已删除的孤立记录
	if cfg.CleanupOrphaned {
		orphaned := s.cleanupOrphanedRecords()
		result.DeletedOrphaned = orphaned
	}

	return result
}

// cleanupOrphanedRecords 清理源文件已删除的孤立记录
// 检查 input_path 对应的文件是否存在，如果不存在则删除记录
// 注意：此操作只针对已完成或失败的任务，不会删除正在处理的任务记录
func (s *SQLiteStorage) cleanupOrphanedRecords() int {
	// 只查询已完成或失败的任务
	query := `SELECT id, input_path FROM process_logs WHERE status IN ('success', 'failed', 'discarded')`
	rows, err := s.db.Query(query)
	if err != nil {
		return 0
	}
	defer rows.Close()

	var idsToDelete []int64
	for rows.Next() {
		var id int64
		var inputPath string
		if err := rows.Scan(&id, &inputPath); err != nil {
			continue
		}

		// 检查源文件是否存在
		if _, err := os.Stat(inputPath); os.IsNotExist(err) {
			idsToDelete = append(idsToDelete, id)
		}
	}

	// 批量删除孤立记录
	if len(idsToDelete) > 0 {
		// 分批删除，每批最多 100 条
		batchSize := 100
		for i := 0; i < len(idsToDelete); i += batchSize {
			end := i + batchSize
			if end > len(idsToDelete) {
				end = len(idsToDelete)
			}
			batch := idsToDelete[i:end]

			// 构建 IN 子句
			placeholders := ""
			args := make([]interface{}, len(batch))
			for j, id := range batch {
				if j > 0 {
					placeholders += ","
				}
				placeholders += "?"
				args[j] = id
			}

			deleteQuery := fmt.Sprintf("DELETE FROM process_logs WHERE id IN (%s)", placeholders)
			s.db.Exec(deleteQuery, args...)
		}
	}

	return len(idsToDelete)
}

// CleanupOrphanedCovers 清理无效的封面历史记录
// 检查 cover_path 对应的文件是否存在，如果不存在则删除记录
func (s *SQLiteStorage) CleanupOrphanedCovers() int {
	query := `SELECT id, cover_path FROM cover_history`
	rows, err := s.db.Query(query)
	if err != nil {
		return 0
	}
	defer rows.Close()

	var idsToDelete []int64
	for rows.Next() {
		var id int64
		var coverPath string
		if err := rows.Scan(&id, &coverPath); err != nil {
			continue
		}

		// 检查封面文件是否存在
		if _, err := os.Stat(coverPath); os.IsNotExist(err) {
			idsToDelete = append(idsToDelete, id)
		}
	}

	// 批量删除
	if len(idsToDelete) > 0 {
		batchSize := 100
		for i := 0; i < len(idsToDelete); i += batchSize {
			end := i + batchSize
			if end > len(idsToDelete) {
				end = len(idsToDelete)
			}
			batch := idsToDelete[i:end]

			placeholders := ""
			args := make([]interface{}, len(batch))
			for j, id := range batch {
				if j > 0 {
					placeholders += ","
				}
				placeholders += "?"
				args[j] = id
			}

			deleteQuery := fmt.Sprintf("DELETE FROM cover_history WHERE id IN (%s)", placeholders)
			s.db.Exec(deleteQuery, args...)
		}
	}

	return len(idsToDelete)
}

// GetDatabaseStats 获取数据库统计信息
func (s *SQLiteStorage) GetDatabaseStats() (map[string]int, error) {
	stats := make(map[string]int)

	// 处理日志统计
	var totalLogs, successLogs, failedLogs, otherLogs int
	s.db.QueryRow(`SELECT COUNT(*) FROM process_logs`).Scan(&totalLogs)
	s.db.QueryRow(`SELECT COUNT(*) FROM process_logs WHERE status = 'success'`).Scan(&successLogs)
	s.db.QueryRow(`SELECT COUNT(*) FROM process_logs WHERE status = 'failed'`).Scan(&failedLogs)
	s.db.QueryRow(`SELECT COUNT(*) FROM process_logs WHERE status NOT IN ('success', 'failed')`).Scan(&otherLogs)

	stats["total_logs"] = totalLogs
	stats["success_logs"] = successLogs
	stats["failed_logs"] = failedLogs
	stats["other_logs"] = otherLogs

	// 封面历史统计
	var totalCovers int
	s.db.QueryRow(`SELECT COUNT(*) FROM cover_history`).Scan(&totalCovers)
	stats["total_covers"] = totalCovers

	return stats, nil
}

// VacuumDatabase 执行数据库 VACUUM 操作
// 用于在大量删除后释放磁盘空间
func (s *SQLiteStorage) VacuumDatabase() error {
	_, err := s.db.Exec("VACUUM")
	if err != nil {
		return fmt.Errorf("VACUUM 失败: %w", err)
	}
	return nil
}

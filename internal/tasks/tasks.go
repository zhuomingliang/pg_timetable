package tasks

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/cybertec-postgresql/pg_timetable/internal/pgengine"
)

// Tasks maps builtin task names with event handlers
var Tasks = map[string](func(string) error){
	"NoOp":     taskNoOp,
	"Sleep":    taskSleep,
	"Log":      taskLog,
	"SendMail": taskSendMail,
	"Download": taskDownloadFile}

// ExecuteTask executes built-in task depending on task name and returns err result
func ExecuteTask(name string, paramValues []string) error {
	pgengine.LogToDB("DEBUG", fmt.Sprintf("Executing builtin task %s with parameters %v", name, paramValues))
	if len(paramValues) == 0 {
		paramValues = append(paramValues, "")
	}
	f := Tasks[name]
	if f == nil {
		return errors.New("No built-in task found: " + name)
	}
	for _, val := range paramValues {
		err := f(val)
		if err != nil {
			return err
		}
	}
	return nil
}

func taskNoOp(val string) error {
	pgengine.LogToDB("DEBUG", "NoOp task called with value: ", val)
	return nil
}

func taskSleep(val string) (err error) {
	var d int
	if d, err = strconv.Atoi(val); err != nil {
		return err
	}
	pgengine.LogToDB("DEBUG", "Sleep task called for ", d, " seconds")
	time.Sleep(time.Duration(d) * time.Second)
	return nil
}

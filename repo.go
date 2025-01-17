package deckard

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// RepoUpdate refreshes all repo based resources after the UI has been started.
func UpdateFromRepo(ui *DeckardUI) {
	go backgroundUpdate(ui)
}

func backgroundUpdate(ui *DeckardUI) {
	updateRepos(ui)
	updateCommits(ui)
}

func updateCommits(ui *DeckardUI) {
	updateStatus(ui, "Updating commit list...")

	commits := make([]*Commit, 0)

	for prj, conf := range ui.config.Projects {
		since, err := GetFetchState(ui.db, prj)
		if err != nil {
			panic(err) //TODO show error in UI
		}
		if since == nil {
			fallback := time.Now().Add(60 * -24 * time.Hour)
			since = &fallback
		}

		folder := repoFolder(ui.config, conf)
		log, err := logRepo(folder, since)
		if err != nil {
			panic(err)
		}

		var lastCommitTime = since
		repoCommits := make([]*Commit, 0)

		for _, commit := range log {
			diff, err := diffRepo(folder, commit.Hash)
			if err != nil {
				panic(fmt.Errorf("diff failed %s, %s, %w", folder, commit.Hash, err)) // TODO show error in UI
			}

			slatScore, err := slatScore(diff)
			if err != nil {
				panic(err) // TODO show error in UI
			}
			commit.Project = prj
			commit.State = STATE_NEW
			commit.SlatScore = slatScore

			// TODO go back to AuthorWhen???
			if commit.CommitWhen.After(*lastCommitTime) {
				lastCommitTime = &commit.CommitWhen
			}

			repoCommits = append(repoCommits, commit)
		}

		err = StoreCommits(ui.db, repoCommits)
		if err != nil {
			panic(err) //TODO show error in UI
		}

		commits = append(commits, repoCommits...)

		err = UpdateFetchState(ui.db, prj, lastCommitTime)
		if err != nil {
			panic(err) //TODO show error in UI
		}
	}

	ui.app.QueueUpdateDraw(func() {
		UpdateFromDB(ui.db, ui)
	})

	clearStatus(ui)
}

// check if repos are there, if not clones them. If they exist
// they are pulled to the latest state.
func updateRepos(ui *DeckardUI) {
	for _, conf := range ui.config.Projects {
		folder := repoFolder(ui.config, conf)
		_, err := os.Stat(folder)
		if os.IsNotExist(err) {
			updateStatus(ui, fmt.Sprintf("Cloning new repo: %s (this may take a while)", conf.Repo))
			err := cloneRepo(conf, folder)
			if err != nil {
				panic(err) //TODO show error in ui instead
			}
		} else {
			updateStatus(ui, fmt.Sprintf("Pulling repo: %s", conf.Repo))
			err := pullRepo(folder)
			if err != nil {
				panic(fmt.Errorf("error pulling %s: %w", folder, err)) //TODO show error in ui instead if this fails
			}
		}
	}
	clearStatus(ui)
}

func repoFolder(conf *Config, prjConf ConfigProject) string {
	return path.Join(conf.CodeFolder, path.Base(prjConf.Repo))
}

func cloneRepo(prj ConfigProject, targetFolder string) error {
	cmd := exec.Command("git", "clone", prj.Repo, targetFolder)
	return cmd.Run()
}

func pullRepo(targetFolder string) error {
	cmd := exec.Command("git", "pull")
	cmd.Dir = targetFolder
	return cmd.Run()
}

func logRepo(targetFolder string, since *time.Time) ([]*Commit, error) {
	sinceArg := fmt.Sprintf("--since=%s", since.Format(time.RFC3339))
	cmd := exec.Command("git", "log", sinceArg, "--format=%H%x00%an%x00%cn%x00%ct%x00%s%x00%b%x00")
	cmd.Dir = targetFolder
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	split := strings.Split(string(out), "\x00")

	commits := make([]*Commit, 0)
	for i := 0; i < len(split); i += 6 {

		if i+6 > len(split) {
			break
		}

		cwUnix, err := strconv.Atoi(split[i+3])
		if err != nil {
			return nil, fmt.Errorf("illegal commit time: %s, folder = %s", split[i+3], targetFolder)
		}

		commits = append(commits, &Commit{
			Hash:          strings.TrimSpace(split[i]),
			AuthorName:    split[i+1],
			CommitterName: split[i+2],
			CommitWhen:    time.Unix(int64(cwUnix), 0),
			Subject:       split[i+4],
			Message:       split[i+5],
		})
	}
	return commits, nil
}

type NumStat struct {
	Added   uint64
	Deleted uint64
	File    string
}

type Diff struct {
	Stats []NumStat
}

func diffRepo(targetFolder, hash string) (*Diff, error) {
	cmd := exec.Command("git", "diff", "--numstat", fmt.Sprintf("%s..%s^", hash, hash), "--")
	cmd.Dir = targetFolder
	diffOut, err := cmd.Output()
	if err != nil {
		errExit := err.(*exec.ExitError)
		if errExit.ExitCode() == 128 { //probably the first commit, return an empty commit
			return &Diff{}, nil
		}
		return nil, fmt.Errorf("diff command failed: %w, out=%s", err, string(errExit.Stderr))
	}

	parsed, err := parseNumStat(string(diffOut))
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseNumStat(raw string) (*Diff, error) {

	if len(raw) == 0 {
		return &Diff{}, nil
	}

	lines := strings.Split(raw, "\n")

	stats := make([]NumStat, 0)
	for _, line := range lines {
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected diff line: %s", line)
		}

		added, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			//TODO At least log an error here
			stats = append(stats, NumStat{Added: 0, Deleted: 0, File: ""})
			continue
		}
		deleted, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			//TODO At least log an error here
			stats = append(stats, NumStat{Added: 0, Deleted: 0, File: ""})
			continue
		}

		file := ""
		for i := 2; i < len(fields); i++ {
			if i != 2 {
				file += " "
			}
			file += fields[i]
		}

		stats = append(stats, NumStat{Added: added, Deleted: deleted, File: file})
	}

	return &Diff{Stats: stats}, nil
}

func updateStatus(ui *DeckardUI, text string) {
	ui.app.QueueUpdateDraw(func() {
		ui.UpdateStatus(text)
	})
}

func clearStatus(ui *DeckardUI) {
	ui.app.QueueUpdateDraw(func() {
		ui.ClearStatus()
	})
}

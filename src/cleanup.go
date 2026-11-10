package main

// Nettoyage du poste : le PC a peu de place. Un lot dont MULCV a confirmé le dépôt est supprimé du PC
// (il est dans le SAE) ; seuls restent, pour un temps, ce qui demande une action de l'opérateur.

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	// Taille au-delà de laquelle le journal est archivé (un seul ancien journal est gardé).
	maxLogBytes = 5 << 20
	// Les lots refusés ou abandonnés (3-erreurs) sont gardés ce nombre de jours par défaut.
	defaultErrorDays = 30
)

// sentDone : le lot est dans le SAE. Par défaut il est supprimé du PC ; avec conserverEnvoyesJours > 0,
// il est gardé dans 2-envoyes et supprimé après ce délai par la tâche d'envoi.
func sentDone(path string) bool {
	if cfg.KeepSentDays > 0 {
		return move(path, cfg.SentDir)
	}
	if err := os.Remove(path); err != nil {
		log.Printf("%s : déposé dans MULCV, mais suppression du PC impossible : %v", filepath.Base(path), err)
		return move(path, cfg.SentDir)
	}
	log.Printf("%s : supprimé du PC (déposé dans MULCV)", filepath.Base(path))
	return true
}

// purge libère la place : lots envoyés et lots en erreur plus anciens que leur délai, et journal trop gros.
func purge(now time.Time) {
	removeOlder(cfg.SentDir, now.AddDate(0, 0, -max(cfg.KeepSentDays, 0)))
	days := cfg.KeepErrorDays
	if days <= 0 {
		days = defaultErrorDays
	}
	removeOlder(cfg.ErrorDir, now.AddDate(0, 0, -days))
	rotateLog()
}

// removeOlder supprime les fichiers et sous-dossiers (lots abandonnés) modifiés avant « before ».
// Les LISEZMOI du dépôt sont gardés.
func removeOlder(dir string, before time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	freed := 0
	for _, entry := range entries {
		if entry.Name() == "LISEZMOI.txt" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(before) {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, entry.Name())) == nil {
			freed++
		}
	}
	if freed > 0 {
		log.Printf("Nettoyage : %d élément(s) ancien(s) supprimé(s) de %s", freed, filepath.Base(dir))
	}
}

// rotateLog garde le journal sous 5 Mo : l'actuel devient mulcv-scan.log.1 (l'ancien .1 est supprimé).
func rotateLog() {
	info, err := os.Stat(cfg.LogFile)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	old := cfg.LogFile + ".1"
	_ = os.Remove(old)
	if err := copyFile(cfg.LogFile, old); err != nil {
		return
	}
	// Vidé sur place : le fichier reste ouvert en ajout par le programme.
	_ = os.Truncate(cfg.LogFile, 0)
	log.Printf("Journal archivé dans %s", filepath.Base(old))
}

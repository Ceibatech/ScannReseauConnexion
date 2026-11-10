package main

// Tâche d'envoi du poste 3 (mulcv-scan.exe -job, lancée par Windows à l'ouverture de session).
//
// L'imprimante prend une feuille à la fois : l'opérateur les numérise l'une après l'autre (fiche du
// carton, étiquette du dossier, pages, séparateurs) et chacune est seulement gardée sur le PC, dans
// numerisation\0-en-cours. Rien ne part tant que le lot n'est pas fini : quand aucune feuille n'est
// arrivée depuis « lotInactiviteMinutes » (10 par défaut), la tâche considère le lot complet,
// l'assemble en un seul PDF dans l'ordre et l'envoie au SAE. Elle renvoie aussi les lots restés dans
// 1-a-envoyer quand MULCV ou le réseau était indisponible.

import (
	"log"
	"os"
	"time"
)

// jobTick est l'intervalle entre deux passages de la tâche.
var jobTick = 15 * time.Second

func (c config) lotIdle() time.Duration {
	if c.LotIdleMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.LotIdleMinutes) * time.Minute
}

// lastPage : date de la feuille la plus récente du lot en cours ; zéro s'il n'y en a pas.
func lastPage() time.Time {
	var last time.Time
	for _, path := range workPages() {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(last) {
			last = info.ModTime()
		}
	}
	return last
}

// finishIdleLot envoie le lot en cours s'il est complet (aucune feuille depuis lotIdle). Renvoie
// true si un lot est parti vers MULCV (ou attend dans 1-a-envoyer).
func finishIdleLot(now time.Time) bool {
	last := lastPage()
	if last.IsZero() || now.Sub(last) < cfg.lotIdle() {
		return false
	}
	unlock := lock()
	defer unlock()
	// Revérifié sous le verrou : une feuille a pu arriver, ou l'opérateur a envoyé le lot (T).
	if last = lastPage(); last.IsZero() || now.Sub(last) < cfg.lotIdle() {
		return false
	}
	path, n, err := assembleLot()
	if err != nil {
		log.Printf("Lot en cours non assemblé : %v", err)
		return false
	}
	if path == "" {
		return false
	}
	log.Printf("Lot complet (aucune feuille depuis %s) : %d page(s) assemblée(s)", cfg.lotIdle(), n)
	process(path)
	return true
}

// runJob tourne jusqu'à l'arrêt de la session.
func runJob() {
	log.Printf("Tâche d'envoi : lot envoyé %s après la dernière feuille", cfg.lotIdle())
	sizes := map[string]int64{}
	for {
		finishIdleLot(time.Now())
		sendStable(cfg.InboxDir, sizes)
		time.Sleep(jobTick)
	}
}

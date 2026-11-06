package main

// Numérisation interactive (numeriser.cmd) : l'opérateur pose les feuilles, appuie sur
// Entrée, et les pages s'ajoutent au lot. « T » assemble le lot en un PDF et l'envoie à
// MULCV, exactement comme un fichier produit par PaperStream sur les postes 1 et 2.
//
// Chaque page reçue est aussitôt écrite dans numerisation\0-en-cours : une coupure
// (programme fermé, PC éteint) ne perd rien, le lot est proposé au lancement suivant.

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// batchPrefix nomme les lots de ce poste ; ce nom n'est jamais pris pour un identifiant de dossier.
const batchPrefix = "LOT-"

func interactive(in io.Reader, out io.Writer) {
	say := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
	lines := bufio.NewScanner(in)
	ask := func(prompt string) (string, bool) {
		fmt.Fprint(out, prompt)
		if !lines.Scan() {
			fmt.Fprintln(out)
			return "Q", false
		}
		return strings.ToUpper(strings.TrimSpace(lines.Text())), true
	}

	say("Poste 3 MULCV : numérisation réseau sur %s", cfg.Scanner.URL)
	sendPending(say)

	var caps capabilities
	for {
		c, err := scannerCapabilities()
		if err == nil {
			caps = c
			break
		}
		say("Scanner injoignable : %v", err)
		say("Vérifier que l'imprimante est allumée, sortie de veille et sur le même réseau que ce PC.")
		if answer, ok := ask("[Entrée] réessayer   [Q] quitter : "); !ok || answer == "Q" {
			return
		}
	}
	say("Scanner prêt : %s.", orUnknown(caps.MakeModel))

	pages := loadWork()
	if len(pages) > 0 {
		say("Un lot interrompu de %d page(s) a été retrouvé.", len(pages))
		answer, ok := ask("[Entrée] le reprendre   [A] l'abandonner (mis de côté dans 3-erreurs) : ")
		if !ok {
			return
		}
		if answer == "A" {
			setAsideWork("lot interrompu abandonné par l'opérateur")
			pages = nil
		}
	}

	say("")
	say("Ordre habituel : fiche du CARTON, étiquette du DOSSIER, pièces du dossier, SÉPARATEUR")
	say("entre deux pièces, étiquette du dossier suivant… Un lot peut contenir tout un carton.")
	for {
		size := 0
		for _, p := range pages {
			size += len(p)
		}
		answer, _ := ask(fmt.Sprintf("\nLot : %d page(s), %.1f Mo.   [Entrée] numériser   [T] terminer et envoyer   [A] annuler le lot   [Q] quitter : ",
			len(pages), float64(size)/(1<<20)))
		switch answer {
		case "":
			if size > maxBytes-(3<<20) {
				say("Lot presque à 25 Mo : tapez T pour l'envoyer, puis continuez dans un nouveau lot")
				say("(MULCV rattache les pages suivantes au dossier en cours).")
				continue
			}
			source := pickSource(caps)
			if source == "Feeder" {
				say("Numérisation du chargeur…")
			} else {
				say("Numérisation de la vitre…")
			}
			got, err := scanJob(caps, source, func(n int) { say("  page %d reçue", len(pages)+n) })
			for _, page := range got {
				pages = append(pages, page)
				if err := saveWorkPage(len(pages), page); err != nil {
					say("  Attention : copie de sécurité de la page impossible : %v", err)
				}
			}
			if err != nil {
				say("  %v", err)
			}
		case "T":
			if len(pages) == 0 {
				say("Le lot est vide.")
				continue
			}
			if finish(pages, say) {
				pages = nil
			}
		case "A":
			if len(pages) == 0 {
				continue
			}
			if confirm, _ := ask(fmt.Sprintf("Abandonner les %d page(s) du lot ? [O/n] : ", len(pages))); confirm == "" || confirm == "O" {
				setAsideWork("lot annulé par l'opérateur")
				pages = nil
				say("Lot mis de côté dans numerisation\\3-erreurs.")
			}
		case "Q":
			if len(pages) > 0 {
				say("Les %d page(s) du lot en cours sont conservées : elles seront proposées au prochain lancement.", len(pages))
			}
			return
		default:
			say("Réponse inconnue : Entrée, T, A ou Q.")
		}
	}
}

// pickSource choisit la vitre ou le chargeur selon la configuration et le papier présent.
func pickSource(c capabilities) string {
	switch {
	case cfg.Scanner.Source == "Platen" && c.Platen, !c.Adf:
		return "Platen"
	case cfg.Scanner.Source == "Feeder", !c.Platen:
		return "Feeder"
	}
	if adfLoaded() {
		return "Feeder"
	}
	return "Platen"
}

// finish assemble le lot en PDF, le dépose dans 1-a-envoyer et l'envoie. Il renvoie true
// quand le lot n'est plus à garder en mémoire (envoyé, refusé ou en attente de réseau).
func finish(pages [][]byte, say func(string, ...any)) bool {
	pdf, err := buildPDF(pages, cfg.Scanner.Resolution)
	if err != nil {
		say("Assemblage du PDF impossible : %v", err)
		return false
	}
	name := batchPrefix + time.Now().Format("20060102-150405") + ".pdf"
	path := filepath.Join(cfg.InboxDir, name)
	// Écrit sous un autre nom puis renommé : la surveillance ne voit jamais un PDF incomplet.
	if err := os.WriteFile(path+".part", pdf, 0o640); err != nil {
		say("Écriture de %s impossible : %v", name, err)
		return false
	}
	if err := os.Rename(path+".part", path); err != nil {
		say("Écriture de %s impossible : %v", name, err)
		return false
	}
	clearWork()
	log.Printf("%s : lot de %d page(s) assemblé", name, len(pages))
	unlock := lock()
	sent := process(path)
	unlock()
	switch {
	case sent:
		say("Lot envoyé à MULCV (détail dans journal\\mulcv-scan.log).")
	case exists(path):
		say("MULCV injoignable : le lot reste dans numerisation\\1-a-envoyer et repartira au prochain lancement.")
	default:
		say("Lot refusé par MULCV : voir numerisation\\3-erreurs\\%s.txt, corriger la pile et la renumériser.", name)
	}
	return true
}

// sendPending renvoie les fichiers restés dans 1-a-envoyer (réseau ou MULCV indisponible).
func sendPending(say func(string, ...any)) {
	entries, err := os.ReadDir(cfg.InboxDir)
	if err != nil {
		return
	}
	var waiting []string
	for _, entry := range entries { // os.ReadDir trie par nom : ordre de numérisation
		if !entry.IsDir() && accepted(entry.Name()) {
			waiting = append(waiting, filepath.Join(cfg.InboxDir, entry.Name()))
		}
	}
	if len(waiting) == 0 {
		return
	}
	say("%d fichier(s) en attente dans 1-a-envoyer : nouvel essai d'envoi.", len(waiting))
	unlock := lock()
	defer unlock()
	for _, path := range waiting {
		if exists(path) {
			process(path)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func saveWorkPage(n int, page []byte) error {
	path := filepath.Join(cfg.WorkDir, fmt.Sprintf("page-%04d.jpg", n))
	if err := os.WriteFile(path+".part", page, 0o640); err != nil {
		return err
	}
	return os.Rename(path+".part", path)
}

func workPages() []string {
	paths, _ := filepath.Glob(filepath.Join(cfg.WorkDir, "page-*.jpg"))
	sort.Strings(paths)
	return paths
}

func loadWork() [][]byte {
	var pages [][]byte
	for _, path := range workPages() {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			pages = append(pages, data)
		}
	}
	return pages
}

func clearWork() {
	for _, path := range workPages() {
		_ = os.Remove(path)
	}
}

// setAsideWork déplace les pages du lot dans 3-erreurs au lieu de les effacer.
func setAsideWork(reason string) {
	target := filepath.Join(cfg.ErrorDir, "lot-abandonne-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(target, 0o750); err != nil {
		log.Printf("Mise de côté du lot impossible : %v", err)
		return
	}
	for _, path := range workPages() {
		move(path, target)
	}
	_ = os.WriteFile(filepath.Join(target, "raison.txt"), []byte(time.Now().Format(time.RFC3339)+" "+reason+"\r\n"), 0o640)
	log.Printf("%s : %s", filepath.Base(target), reason)
}

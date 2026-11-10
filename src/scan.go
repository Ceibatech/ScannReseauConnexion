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

	if waiting := len(workPages()); waiting > 0 {
		say("Un lot de %d page(s) est en cours : les feuilles numérisées maintenant s'y ajoutent.", waiting)
	}

	idle := cfg.lotIdle()
	say("")
	say("Une feuille à la fois sur la vitre : fiche du CARTON, étiquette du DOSSIER, pages du dossier,")
	say("SÉPARATEUR entre deux pièces, dossier suivant… Rien ne part tant que le lot n'est pas fini :")
	say("%s après la dernière feuille, le lot complet est envoyé tout seul au SAE.", minutes(idle))
	for {
		// Le lot est relu sur le disque à chaque tour : la tâche d'envoi a pu l'expédier entre-temps.
		pages := workPages()
		size := totalSize(pages)
		answer, _ := ask(fmt.Sprintf("\nLot : %d page(s), %.1f Mo.   [Entrée] numériser la feuille   [T] envoyer maintenant   [A] annuler le lot   [Q] quitter : ",
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
				say("Numérisation de la feuille…")
			}
			got, err := scanJob(caps, source, func(n int) { say("  page %d reçue", len(pages)+n) })
			for _, page := range got {
				if err := saveWorkPage(page); err != nil {
					say("  Page non enregistrée sur le PC, à renumériser : %v", err)
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
			sendLot(say)
		case "A":
			if len(pages) == 0 {
				continue
			}
			if confirm, _ := ask(fmt.Sprintf("Abandonner les %d page(s) du lot ? [O/n] : ", len(pages))); confirm == "" || confirm == "O" {
				setAsideWork("lot annulé par l'opérateur")
				say("Lot mis de côté dans numerisation\\3-erreurs.")
			}
		case "Q":
			if len(pages) > 0 {
				say("Les %d page(s) restent sur le PC : le lot partira tout seul %s après la dernière feuille,", len(pages), minutes(idle))
				say("ou complétez-le au prochain lancement.")
			}
			return
		default:
			say("Réponse inconnue : Entrée, T, A ou Q.")
		}
	}
}

func minutes(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d secondes", int(d.Seconds()))
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func totalSize(paths []string) int64 {
	var size int64
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			size += info.Size()
		}
	}
	return size
}

// sendLot assemble le lot en cours et l'envoie, sous le verrou d'envoi du poste.
func sendLot(say func(string, ...any)) {
	unlock := lock()
	defer unlock()
	path, n, err := assembleLot()
	if err != nil {
		say("Assemblage du lot impossible : %v", err)
		return
	}
	if path == "" {
		say("Le lot est vide (déjà envoyé par la tâche d'envoi ?).")
		return
	}
	name := filepath.Base(path)
	log.Printf("%s : lot de %d page(s) assemblé", name, n)
	sent := process(path)
	switch {
	case sent:
		say("Lot envoyé à MULCV (détail dans journal\\mulcv-scan.log).")
	case exists(path):
		say("MULCV injoignable : le lot reste dans numerisation\\1-a-envoyer et repartira tout seul.")
	default:
		say("Lot refusé par MULCV : voir numerisation\\3-erreurs\\%s.txt, corriger la pile et la renumériser.", name)
	}
}

// assembleLot fait un PDF des pages en cours (dans l'ordre de numérisation), le dépose dans
// 1-a-envoyer et retire ces pages. À appeler sous le verrou d'envoi. Chemin vide : rien à assembler.
func assembleLot() (string, int, error) {
	paths := workPages()
	if len(paths) == 0 {
		return "", 0, nil
	}
	var pages [][]byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", 0, err
		}
		if len(data) > 0 {
			pages = append(pages, data)
		}
	}
	pdf, err := buildPDF(pages, cfg.Scanner.Resolution)
	if err != nil {
		return "", 0, err
	}
	name := batchPrefix + time.Now().Format("20060102-150405") + ".pdf"
	path := filepath.Join(cfg.InboxDir, name)
	for i := 1; exists(path); i++ {
		path = filepath.Join(cfg.InboxDir, fmt.Sprintf("%s%s-%d.pdf", batchPrefix, time.Now().Format("20060102-150405"), i))
	}
	// Écrit sous un autre nom puis renommé : la surveillance ne voit jamais un PDF incomplet.
	if err := os.WriteFile(path+".part", pdf, 0o640); err != nil {
		return "", 0, err
	}
	if err := os.Rename(path+".part", path); err != nil {
		return "", 0, err
	}
	for _, page := range paths {
		_ = os.Remove(page)
	}
	return path, len(pages), nil
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

// saveWorkPage écrit une feuille du lot en cours, numérotée à la suite.
func saveWorkPage(page []byte) error {
	n := 1
	for _, path := range workPages() {
		var k int
		if _, err := fmt.Sscanf(filepath.Base(path), "page-%d.jpg", &k); err == nil && k >= n {
			n = k + 1
		}
	}
	path := filepath.Join(cfg.WorkDir, fmt.Sprintf("page-%04d.jpg", n))
	if err := os.WriteFile(path+".part", page, 0o640); err != nil {
		return err
	}
	return os.Rename(path+".part", path)
}

// workPages : les feuilles du lot en cours, dans l'ordre de numérisation. Ce sont les pages
// numérisées par ce programme et les JPEG qu'y dépose l'imprimante (« numériser vers un dossier »).
func workPages() []string {
	entries, err := os.ReadDir(cfg.WorkDir)
	if err != nil {
		return nil
	}
	type page struct {
		path string
		at   time.Time
	}
	var pages []page
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if entry.IsDir() || (ext != ".jpg" && ext != ".jpeg") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Size() > 0 {
			pages = append(pages, page{filepath.Join(cfg.WorkDir, entry.Name()), info.ModTime()})
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if !pages[i].at.Equal(pages[j].at) {
			return pages[i].at.Before(pages[j].at)
		}
		return pages[i].path < pages[j].path
	})
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.path
	}
	return out
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

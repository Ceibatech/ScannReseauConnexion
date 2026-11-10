package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Capacités sur le modèle de celles d'une HP LaserJet MFP M236dw (vitre seule).
const platenCaps = `<?xml version="1.0" encoding="UTF-8"?>
<scan:ScannerCapabilities xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
 <pwg:Version>2.63</pwg:Version>
 <pwg:MakeAndModel>HP LaserJet MFP M232-M237</pwg:MakeAndModel>
 <scan:Platen><scan:PlatenInputCaps>
  <scan:MinWidth>8</scan:MinWidth><scan:MaxWidth>2550</scan:MaxWidth>
  <scan:MinHeight>8</scan:MinHeight><scan:MaxHeight>3508</scan:MaxHeight>
  <scan:SettingProfiles><scan:SettingProfile>
   <scan:ColorModes><scan:ColorMode>BlackAndWhite1</scan:ColorMode><scan:ColorMode>Grayscale8</scan:ColorMode><scan:ColorMode>RGB24</scan:ColorMode></scan:ColorModes>
   <scan:DocumentFormats><pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat><pwg:DocumentFormat>application/pdf</pwg:DocumentFormat><scan:DocumentFormatExt>image/jpeg</scan:DocumentFormatExt></scan:DocumentFormats>
   <scan:SupportedResolutions><scan:DiscreteResolutions>
    <scan:DiscreteResolution><scan:XResolution>200</scan:XResolution><scan:YResolution>200</scan:YResolution></scan:DiscreteResolution>
    <scan:DiscreteResolution><scan:XResolution>300</scan:XResolution><scan:YResolution>300</scan:YResolution></scan:DiscreteResolution>
    <scan:DiscreteResolution><scan:XResolution>600</scan:XResolution><scan:YResolution>600</scan:YResolution></scan:DiscreteResolution>
   </scan:DiscreteResolutions></scan:SupportedResolutions>
  </scan:SettingProfile></scan:SettingProfiles>
 </scan:PlatenInputCaps></scan:Platen>
</scan:ScannerCapabilities>`

const adfCaps = `<scan:ScannerCapabilities xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
 <pwg:Version>2.1</pwg:Version>
 <scan:Platen><scan:PlatenInputCaps><scan:MaxWidth>2550</scan:MaxWidth><scan:MaxHeight>3508</scan:MaxHeight>
  <scan:SettingProfiles><scan:SettingProfile><scan:ColorModes><scan:ColorMode>Grayscale8</scan:ColorMode></scan:ColorModes>
  <scan:DocumentFormats><pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat></scan:DocumentFormats></scan:SettingProfile></scan:SettingProfiles>
 </scan:PlatenInputCaps></scan:Platen>
 <scan:Adf><scan:AdfSimplexInputCaps><scan:MaxWidth>2550</scan:MaxWidth><scan:MaxHeight>4200</scan:MaxHeight></scan:AdfSimplexInputCaps></scan:Adf>
</scan:ScannerCapabilities>`

func page(t *testing.T, gray bool, shade uint8) []byte {
	t.Helper()
	var img image.Image
	if gray {
		g := image.NewGray(image.Rect(0, 0, 248, 350))
		for i := range g.Pix {
			g.Pix[i] = shade
		}
		img = g
	} else {
		c := image.NewRGBA(image.Rect(0, 0, 248, 350))
		for y := 0; y < 350; y++ {
			for x := 0; x < 248; x++ {
				c.Set(x, y, color.RGBA{shade, 10, 200, 255})
			}
		}
		img = c
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeScanner simule le serveur eSCL de l'imprimante.
type fakeScanner struct {
	mu        sync.Mutex
	caps      string
	adfLoaded bool
	perJob    int    // pages renvoyées par travail
	location  string // modèle de l'en-tête Location (%d = numéro du travail)
	settings  []string
	busyOnce  bool
	jobs      int
	served    map[int]int
	conflict  bool
}

func (f *fakeScanner) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScannerCapabilities":
			io.WriteString(w, f.caps)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScannerStatus":
			state := "ScannerAdfEmpty"
			if f.adfLoaded {
				state = "ScannerAdfLoaded"
			}
			fmt.Fprintf(w, `<scan:ScannerStatus><pwg:State>Idle</pwg:State><scan:AdfState>%s</scan:AdfState></scan:ScannerStatus>`, state)
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			body, _ := io.ReadAll(r.Body)
			f.settings = append(f.settings, string(body))
			if f.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.jobs++
			w.Header().Set("Location", fmt.Sprintf(f.location, f.jobs))
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/NextDocument"):
			job, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(r.URL.Path, "/eSCL/ScanJobs/"), "/")[0])
			if f.busyOnce {
				f.busyOnce = false
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if f.served[job] >= f.perJob {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.served[job]++
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(page(t, true, uint8(40*f.served[job])))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func withScanner(t *testing.T, f *fakeScanner) {
	t.Helper()
	if f.served == nil {
		f.served = map[int]int{}
	}
	if f.location == "" {
		f.location = "/eSCL/ScanJobs/%d"
	}
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	cfg.Scanner = scannerConfig{URL: server.URL}
	if err := cfg.Scanner.normalize(); err != nil {
		t.Fatal(err)
	}
	scannerClient = server.Client()
	scannerPoll = time.Millisecond
}

func TestTheScannerAddressAcceptsAPlainNetworkName(t *testing.T) {
	for in, want := range map[string]string{
		"NPI3F8C0F":                     "http://NPI3F8C0F/eSCL",
		" 192.168.1.40 ":                "http://192.168.1.40/eSCL",
		"https://NPI3F8C0F.local/eSCL/": "https://NPI3F8C0F.local/eSCL",
	} {
		s := scannerConfig{URL: in}
		if err := s.normalize(); err != nil || s.URL != want {
			t.Fatalf("%q -> %q (%v)", in, s.URL, err)
		}
		if s.Resolution != 300 || s.ColorMode != "Grayscale8" || s.Paper != "A4" || s.Source != "auto" {
			t.Fatalf("réglages par défaut : %+v", s)
		}
	}
	for _, bad := range []scannerConfig{{}, {URL: "ftp://x"}, {URL: "x", ColorMode: "BlackAndWhite1"}, {URL: "x", Source: "Recto"}} {
		if bad.normalize() == nil {
			t.Fatalf("%+v doit être refusé", bad)
		}
	}
}

func TestCapabilitiesOfTheM236dw(t *testing.T) {
	c, err := parseCapabilities([]byte(platenCaps))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Platen || c.Adf || c.Version != "2.63" || c.MaxW["Platen"] != 2550 || c.MaxH["Platen"] != 3508 {
		t.Fatalf("%+v", c)
	}
	if len(c.Resolutions) != 3 || nearest(c.Resolutions, 250) != 200 || nearest(c.Resolutions, 300) != 300 {
		t.Fatalf("résolutions %v", c.Resolutions)
	}
	a, _ := parseCapabilities([]byte(adfCaps))
	if !a.Adf || a.MaxH["Feeder"] != 4200 || a.MaxH["Platen"] != 3508 {
		t.Fatalf("chargeur : %+v", a)
	}
	if _, err := parseCapabilities([]byte(`<x/>`)); err == nil {
		t.Fatal("sans source, le scanner est inutilisable")
	}
}

func TestAPlatenScanWaitsForThePageAndKeepsTheConfiguredHost(t *testing.T) {
	// L'imprimante répond avec son adresse IP interne : le programme garde l'adresse configurée.
	f := &fakeScanner{caps: platenCaps, perJob: 1, busyOnce: true, location: "http://10.255.0.9:8080/eSCL/ScanJobs/%d"}
	withScanner(t, f)
	c, _ := scannerCapabilities()
	pages, err := scanJob(c, pickSource(c), nil)
	if err != nil || len(pages) != 1 {
		t.Fatalf("%d page(s), %v", len(pages), err)
	}
	s := f.settings[0]
	for _, want := range []string{"<pwg:InputSource>Platen<", "<scan:ColorMode>Grayscale8<", "<scan:XResolution>300<", "<pwg:Width>2480<", "<pwg:Height>3508<", "<pwg:Version>2.63<", "image/jpeg"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q absent des réglages :\n%s", want, s)
		}
	}
	if err := checkScanner(); err != nil {
		t.Fatal(err)
	}
}

func TestALoadedFeederIsScannedWhole(t *testing.T) {
	f := &fakeScanner{caps: adfCaps, perJob: 3, adfLoaded: true}
	withScanner(t, f)
	c, _ := scannerCapabilities()
	var seen []int
	pages, err := scanJob(c, pickSource(c), func(n int) { seen = append(seen, n) })
	if err != nil || len(pages) != 3 || len(seen) != 3 || !strings.Contains(f.settings[0], "<pwg:InputSource>Feeder<") {
		t.Fatalf("%d page(s), %v, %v", len(pages), err, seen)
	}
	f.adfLoaded = false
	if pickSource(c) != "Platen" {
		t.Fatal("chargeur vide : la vitre doit être utilisée")
	}
	f.conflict = true
	if _, err := scanJob(c, "Feeder", nil); err != errAdfEmpty {
		t.Fatalf("chargeur vide : %v", err)
	}
}

func TestAPdfIsAssembledInScanOrderWithoutReencoding(t *testing.T) {
	pages := [][]byte{page(t, true, 10), page(t, false, 90), page(t, true, 200)}
	pdf, err := buildPDF(pages, 300)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("en-tête ou fin de PDF absents")
	}
	if n := bytes.Count(pdf, []byte("/Type /Page ")); n != 3 {
		t.Fatalf("%d pages", n)
	}
	for i, p := range pages {
		at := bytes.Index(pdf, p)
		if at < 0 {
			t.Fatalf("page %d réencodée", i+1)
		}
		if i > 0 && at < bytes.Index(pdf, pages[i-1]) {
			t.Fatal("ordre des pages perdu")
		}
	}
	if !bytes.Contains(pdf, []byte("/DeviceGray")) || !bytes.Contains(pdf, []byte("/DeviceRGB")) {
		t.Fatal("espaces de couleur")
	}
	// 248 x 350 pixels à 300 ppp (densité JFIF absente) = 59.52 x 84 points.
	if !bytes.Contains(pdf, []byte("/MediaBox [0 0 59.52 84.00]")) {
		t.Fatal("taille de page")
	}
	// Chaque entrée de la table xref pointe sur le début de son objet.
	xref := pdf[bytes.LastIndex(pdf, []byte("\nxref\n")):]
	entries := regexp.MustCompile(`(\d{10}) 00000 n`).FindAllSubmatch(xref, -1)
	if len(entries) != 2+3*3 {
		t.Fatalf("%d objets", len(entries))
	}
	for i, e := range entries {
		offset, _ := strconv.Atoi(string(e[1]))
		if !bytes.HasPrefix(pdf[offset:], []byte(fmt.Sprintf("%d 0 obj", i+1))) {
			t.Fatalf("xref de l'objet %d fausse", i+1)
		}
	}
	start := regexp.MustCompile(`startxref\n(\d+)`).FindSubmatch(pdf)
	if at, _ := strconv.Atoi(string(start[1])); !bytes.HasPrefix(pdf[at:], []byte("xref")) {
		t.Fatal("startxref faux")
	}
}

func TestTheJfifDensityGivesThePaperSize(t *testing.T) {
	jfif := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 1, 0x01, 0x2C, 0x01, 0x2C}
	if x, y := jpegDPI(jfif); x != 300 || y != 300 {
		t.Fatalf("%d %d", x, y)
	}
	jfif[13] = 0
	if x, _ := jpegDPI(jfif); x != 0 {
		t.Fatal("densité sans unité ignorée")
	}
}

// station prépare un poste complet : faux MULCV (HTTPS) et faux scanner (eSCL).
func station(t *testing.T, f *fakeScanner) (dir string, received *[]string, hints *[]string) {
	received, hints = &[]string{}, &[]string{}
	dir = setup(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		r.ParseMultipartForm(32 << 20)
		file := r.MultipartForm.File["file"][0]
		in, _ := file.Open()
		data, _ := io.ReadAll(in)
		*received = append(*received, fmt.Sprintf("%s:%d", file.Filename, bytes.Count(data, []byte("/Type /Page "))))
		if v, ok := r.MultipartForm.Value["dossierId"]; ok {
			*hints = append(*hints, v[0])
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"kind":"dossier","documents":[{"dossierId":"2010-KOU-0201-001","cartonUid":"2010-KOU-0201","identifiedBy":"qr","pages":"2-3"}]}`)
	})
	cfg.InboxDir, cfg.WorkDir, cfg.LogFile = filepath.Join(dir, "a-envoyer"), filepath.Join(dir, "en-cours"), filepath.Join(dir, "scan.log")
	os.MkdirAll(cfg.InboxDir, 0o750)
	os.MkdirAll(cfg.WorkDir, 0o750)
	withScanner(t, f)
	return
}

func TestABatchOfFlatbedPagesBecomesOneDepositedPdf(t *testing.T) {
	dir, received, hints := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	var out bytes.Buffer
	interactive(strings.NewReader("\n\n\nT\nQ\n"), &out)
	if len(*received) != 1 || !regexp.MustCompile(`^LOT-\d{8}-\d{6}\.pdf:3$`).MatchString((*received)[0]) {
		t.Fatalf("reçu par MULCV : %v\n%s", *received, out.String())
	}
	if len(*hints) != 0 {
		t.Fatalf("le nom d'un lot n'est pas un dossier : %v", *hints)
	}
	if len(workPages()) != 0 {
		t.Fatal("les pages de travail doivent être effacées après l'envoi")
	}
	kept, _ := filepath.Glob(filepath.Join(dir, "*", "LOT-*.pdf"))
	if len(kept) != 0 || !strings.Contains(out.String(), "Lot envoyé à MULCV") {
		t.Fatalf("aucune copie du lot ne reste sur le PC : %v\n%s", kept, out.String())
	}
}

func TestAnInterruptedBatchIsResumedAtNextLaunch(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	// Premier lancement : deux pages puis fermeture sans envoyer.
	interactive(strings.NewReader("\n\nQ\n"), io.Discard)
	if len(workPages()) != 2 || len(*received) != 0 {
		t.Fatalf("pages conservées : %d", len(workPages()))
	}
	// Second lancement : les feuilles s'ajoutent au lot en cours, puis envoi.
	var out bytes.Buffer
	interactive(strings.NewReader("\nT\n"), &out)
	if len(*received) != 1 || !strings.HasSuffix((*received)[0], ":3") || !strings.Contains(out.String(), "lot de 2 page(s) est en cours") {
		t.Fatalf("reçu : %v\n%s", *received, out.String())
	}
}

func TestAnAbandonedBatchIsSetAsideNotDeleted(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	interactive(strings.NewReader("\n\nA\nO\nQ\n"), io.Discard)
	kept, _ := filepath.Glob(filepath.Join(cfg.ErrorDir, "lot-abandonne-*", "page-*.jpg"))
	if len(kept) != 2 || len(*received) != 0 || len(workPages()) != 0 {
		t.Fatalf("pages mises de côté : %v", kept)
	}
}

func TestABatchWaitingForTheNetworkLeavesAtNextLaunch(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	waiting := filepath.Join(cfg.InboxDir, "LOT-20261106-170000.pdf")
	pdf, _ := buildPDF([][]byte{page(t, true, 1)}, 300)
	os.WriteFile(waiting, pdf, 0o640)
	os.WriteFile(filepath.Join(cfg.InboxDir, "LOT-x.pdf.part"), pdf, 0o640) // jamais envoyé : incomplet
	interactive(strings.NewReader("Q\n"), io.Discard)
	if len(*received) != 1 || !strings.HasPrefix((*received)[0], "LOT-20261106-170000.pdf") {
		t.Fatalf("reçu : %v", *received)
	}
}

// age recule la date des feuilles du lot en cours, comme si la dernière datait de « ago ».
func age(t *testing.T, ago time.Duration) {
	for i, path := range workPages() {
		at := time.Now().Add(-ago - time.Duration(len(workPages())-i)*time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheJobSendsALotOnlyOnceItIsComplete(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	cfg.LotIdleMinutes = 10
	// L'opérateur numérise trois feuilles une à une et quitte sans rien taper pour envoyer.
	interactive(strings.NewReader("\n\n\nQ\n"), io.Discard)
	if finishIdleLot(time.Now()) || len(*received) != 0 {
		t.Fatal("la dernière feuille date d'à l'instant : le lot n'est pas fini, rien ne part")
	}
	age(t, 9*time.Minute)
	if finishIdleLot(time.Now()) || len(*received) != 0 {
		t.Fatal("9 minutes : toujours en cours")
	}
	age(t, 11*time.Minute)
	if !finishIdleLot(time.Now()) || len(*received) != 1 || !strings.HasSuffix((*received)[0], ":3") {
		t.Fatalf("lot complet envoyé en un seul PDF : %v", *received)
	}
	if len(workPages()) != 0 || finishIdleLot(time.Now()) || len(*received) != 1 {
		t.Fatal("un lot ne part qu'une fois")
	}
}

func TestPagesDroppedByThePrinterJoinTheLotInScanOrder(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	// « Numériser vers un dossier » depuis l'écran de l'imprimante : des JPEG au nom libre.
	for i, name := range []string{"scan_0003.jpg", "scan_0001.JPG", "scan_0002.jpeg"} {
		path := filepath.Join(cfg.WorkDir, name)
		os.WriteFile(path, page(t, true, uint8(50*(i+1))), 0o640)
		at := time.Now().Add(-time.Hour + time.Duration([]int{3, 1, 2}[i])*time.Minute)
		os.Chtimes(path, at, at)
	}
	os.WriteFile(filepath.Join(cfg.WorkDir, "LISEZMOI.txt"), []byte("x"), 0o640)
	order := workPages()
	if len(order) != 3 || filepath.Base(order[0]) != "scan_0001.JPG" || filepath.Base(order[2]) != "scan_0003.jpg" {
		t.Fatalf("ordre de numérisation (date) : %v", order)
	}
	if !finishIdleLot(time.Now()) || len(*received) != 1 || !strings.HasSuffix((*received)[0], ":3") {
		t.Fatalf("reçu : %v", *received)
	}
}

func TestTheOperatorNeverResendsALotTheJobAlreadySent(t *testing.T) {
	_, received, _ := station(t, &fakeScanner{caps: platenCaps, perJob: 1})
	cfg.LotIdleMinutes = 1
	interactive(strings.NewReader("\n\nQ\n"), io.Discard)
	age(t, 2*time.Minute)
	finishIdleLot(time.Now())
	var out bytes.Buffer
	interactive(strings.NewReader("T\nQ\n"), &out)
	if len(*received) != 1 || !strings.Contains(out.String(), "Le lot est vide") {
		t.Fatalf("reçu : %v\n%s", *received, out.String())
	}
}

func TestTheIdleDelayIsBounded(t *testing.T) {
	if (config{}).lotIdle() != 10*time.Minute || (config{LotIdleMinutes: 3}).lotIdle() != 3*time.Minute {
		t.Fatal("délai d'inactivité")
	}
}

func TestOldFilesAreCleanedBecauseThePCIsShortOfSpace(t *testing.T) {
	dir := t.TempDir()
	cfg = config{SentDir: filepath.Join(dir, "envoyes"), ErrorDir: filepath.Join(dir, "erreurs"), LogFile: filepath.Join(dir, "scan.log"), KeepSentDays: 7}
	os.MkdirAll(cfg.SentDir, 0o750)
	os.MkdirAll(filepath.Join(cfg.ErrorDir, "lot-abandonne-old"), 0o750)
	write := func(path string, age time.Duration) {
		os.WriteFile(path, []byte("x"), 0o640)
		at := time.Now().Add(-age)
		os.Chtimes(path, at, at)
	}
	write(filepath.Join(cfg.SentDir, "LOT-vieux.pdf"), 8*24*time.Hour)
	write(filepath.Join(cfg.SentDir, "LOT-recent.pdf"), 2*24*time.Hour)
	write(filepath.Join(cfg.SentDir, "LISEZMOI.txt"), 400*24*time.Hour)
	write(filepath.Join(cfg.ErrorDir, "LOT-refuse.pdf"), 31*24*time.Hour)
	write(filepath.Join(cfg.ErrorDir, "LOT-refuse-recent.pdf"), 3*24*time.Hour)
	old := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(filepath.Join(cfg.ErrorDir, "lot-abandonne-old"), old, old)
	os.WriteFile(cfg.LogFile, bytes.Repeat([]byte("ligne\n"), maxLogBytes/6+10), 0o640)
	purge(time.Now())
	left := func(d string) []string {
		e, _ := os.ReadDir(d)
		var n []string
		for _, x := range e {
			n = append(n, x.Name())
		}
		return n
	}
	if got := strings.Join(left(cfg.SentDir), ","); got != "LISEZMOI.txt,LOT-recent.pdf" {
		t.Fatalf("envoyés : %s", got)
	}
	if got := strings.Join(left(cfg.ErrorDir), ","); got != "LOT-refuse-recent.pdf" {
		t.Fatalf("erreurs (30 jours) : %s", got)
	}
	if info, _ := os.Stat(cfg.LogFile); info.Size() != 0 {
		t.Fatal("journal trop gros : vidé")
	}
	if info, err := os.Stat(cfg.LogFile + ".1"); err != nil || info.Size() < maxLogBytes {
		t.Fatal("ancien journal gardé une fois")
	}
	// Par défaut (0 jour), rien n'est gardé dans 2-envoyes.
	cfg.KeepSentDays = 0
	purge(time.Now())
	if got := strings.Join(left(cfg.SentDir), ","); got != "LISEZMOI.txt" {
		t.Fatalf("envoyés, défaut : %s", got)
	}
}

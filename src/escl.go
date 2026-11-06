package main

// Pilotage d'un scanner réseau par eSCL (« AirScan », Mopria), le protocole HTTP que les
// multifonctions HP (dont la LaserJet MFP M236dw) exposent sous /eSCL :
//
//	GET  /eSCL/ScannerCapabilities   sources (vitre, chargeur), formats, résolutions
//	GET  /eSCL/ScannerStatus         état du scanner et du chargeur
//	POST /eSCL/ScanJobs              lance une numérisation, répond 201 + Location
//	GET  <Location>/NextDocument     une page JPEG par appel ; 404 quand il n'y en a plus

import (
	"bytes"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type scannerConfig struct {
	// Adresse du scanner : nom réseau (NPI3F8C0F), adresse IP ou URL complète.
	URL string `json:"url"`
	// Résolution en points par pouce (300 conseillé pour l'OCR et la lecture des QR).
	Resolution int `json:"resolution"`
	// Grayscale8 (niveaux de gris, conseillé) ou RGB24 (couleur). Mode fixe, comme PaperStream.
	ColorMode string `json:"colorMode"`
	// A4, Letter ou Legal.
	Paper string `json:"paper"`
	// auto (chargeur s'il est garni, sinon vitre), Platen (vitre) ou Feeder (chargeur).
	Source string `json:"source"`
	// Certificat auto-signé du scanner accepté (seulement pour une adresse https).
	InsecureTLS bool `json:"insecureTls"`
}

func (s *scannerConfig) normalize() error {
	raw := strings.TrimSpace(s.URL)
	if raw == "" {
		return errors.New("url obligatoire (nom réseau ou adresse IP du scanner)")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("adresse invalide : %s", s.URL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/eSCL"
	}
	u.RawQuery, u.Fragment = "", ""
	s.URL = u.String()
	if s.Resolution == 0 {
		s.Resolution = 300
	}
	if s.Resolution < 75 || s.Resolution > 1200 {
		return fmt.Errorf("résolution %d hors limites", s.Resolution)
	}
	switch s.ColorMode {
	case "":
		s.ColorMode = "Grayscale8"
	case "Grayscale8", "RGB24":
	default:
		return fmt.Errorf("colorMode %q : Grayscale8 ou RGB24", s.ColorMode)
	}
	if s.Paper == "" {
		s.Paper = "A4"
	}
	if _, ok := papers[s.Paper]; !ok {
		return fmt.Errorf("paper %q : A4, Letter ou Legal", s.Paper)
	}
	switch s.Source {
	case "":
		s.Source = "auto"
	case "auto", "Platen", "Feeder":
	default:
		return fmt.Errorf("source %q : auto, Platen ou Feeder", s.Source)
	}
	return nil
}

// Formats en trois-centièmes de pouce (unité des zones eSCL).
var papers = map[string][2]int{"A4": {2480, 3508}, "Letter": {2550, 3300}, "Legal": {2550, 4200}}

type capabilities struct {
	Version     string
	MakeModel   string
	Platen, Adf bool
	MaxW, MaxH  map[string]int // par source : "Platen", "Feeder"
	ColorModes  []string
	Resolutions []int
	Formats     []string
}

var scannerClient = &http.Client{Timeout: 3 * time.Minute}

func scannerHTTP() *http.Client {
	if cfg.Scanner.InsecureTLS && strings.HasPrefix(cfg.Scanner.URL, "https:") {
		scannerClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec : réseau local, scanner à certificat auto-signé
	}
	return scannerClient
}

func scannerGet(path string) (int, []byte, error) {
	response, err := scannerHTTP().Get(cfg.Scanner.URL + path)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	return response.StatusCode, data, err
}

// parseCapabilities lit les éléments utiles sans dépendre des espaces de noms (variables selon les modèles).
func parseCapabilities(data []byte) (capabilities, error) {
	c := capabilities{MaxW: map[string]int{}, MaxH: map[string]int{}}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	source := ""
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return c, fmt.Errorf("réponse du scanner illisible : %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			switch t.Name.Local {
			case "Platen":
				c.Platen, source = true, "Platen"
			case "Adf":
				c.Adf, source = true, "Feeder"
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if t.Name.Local == "Platen" || t.Name.Local == "Adf" {
				source = ""
			}
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			text := strings.TrimSpace(string(t))
			if text == "" {
				continue
			}
			switch stack[len(stack)-1] {
			case "Version":
				if len(stack) == 2 {
					c.Version = text
				}
			case "MakeAndModel":
				c.MakeModel = text
			case "MaxWidth":
				if n, err := strconv.Atoi(text); err == nil && source != "" && c.MaxW[source] == 0 {
					c.MaxW[source] = n
				}
			case "MaxHeight":
				if n, err := strconv.Atoi(text); err == nil && source != "" && c.MaxH[source] == 0 {
					c.MaxH[source] = n
				}
			case "ColorMode":
				if !slices.Contains(c.ColorModes, text) {
					c.ColorModes = append(c.ColorModes, text)
				}
			case "XResolution":
				if n, err := strconv.Atoi(text); err == nil && !slices.Contains(c.Resolutions, n) {
					c.Resolutions = append(c.Resolutions, n)
				}
			case "DocumentFormat", "DocumentFormatExt":
				if !slices.Contains(c.Formats, text) {
					c.Formats = append(c.Formats, text)
				}
			}
		}
	}
	if !c.Platen && !c.Adf {
		return c, errors.New("le scanner n'annonce ni vitre ni chargeur")
	}
	return c, nil
}

func scannerCapabilities() (capabilities, error) {
	status, data, err := scannerGet("/ScannerCapabilities")
	if err != nil {
		return capabilities{}, err
	}
	if status != http.StatusOK {
		return capabilities{}, fmt.Errorf("ScannerCapabilities : HTTP %d (eSCL désactivé ? l'activer dans le serveur web de l'imprimante, onglet Réseau > AirPrint/Mopria)", status)
	}
	return parseCapabilities(data)
}

// adfLoaded indique si du papier est posé dans le chargeur (faux si l'état est inconnu).
func adfLoaded() bool {
	status, data, err := scannerGet("/ScannerStatus")
	if err != nil || status != http.StatusOK {
		return false
	}
	return bytes.Contains(data, []byte("ScannerAdfLoaded"))
}

// checkScanner est appelé par -test : il échoue si le poste ne pourra pas numériser.
func checkScanner() error {
	c, err := scannerCapabilities()
	if err != nil {
		return err
	}
	if !slices.Contains(c.Formats, "image/jpeg") {
		return fmt.Errorf("le scanner ne propose pas le JPEG (formats : %s)", strings.Join(c.Formats, ", "))
	}
	if len(c.ColorModes) > 0 && !slices.Contains(c.ColorModes, cfg.Scanner.ColorMode) {
		return fmt.Errorf("mode %s non proposé (modes : %s)", cfg.Scanner.ColorMode, strings.Join(c.ColorModes, ", "))
	}
	sources := []string{}
	if c.Platen {
		sources = append(sources, "vitre")
	}
	if c.Adf {
		sources = append(sources, "chargeur")
	}
	log.Printf("Scanner %s joignable : %s, eSCL %s, %s, %d ppp %s.", cfg.Scanner.URL, orUnknown(c.MakeModel), orUnknown(c.Version),
		strings.Join(sources, " + "), nearest(c.Resolutions, cfg.Scanner.Resolution), cfg.Scanner.ColorMode)
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "modèle inconnu"
	}
	return s
}

// nearest renvoie la résolution proposée la plus proche de celle demandée.
func nearest(offered []int, wanted int) int {
	best := wanted
	for i, r := range offered {
		if i == 0 || abs(r-wanted) < abs(best-wanted) {
			best = r
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func scanSettings(c capabilities, source string) []byte {
	size := papers[cfg.Scanner.Paper]
	w, h := size[0], size[1]
	if m := c.MaxW[source]; m > 0 && w > m {
		w = m
	}
	if m := c.MaxH[source]; m > 0 && h > m {
		h = m
	}
	version := c.Version
	if version == "" {
		version = "2.0"
	}
	resolution := nearest(c.Resolutions, cfg.Scanner.Resolution)
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<scan:ScanSettings xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
  <pwg:Version>%s</pwg:Version>
  <scan:Intent>Document</scan:Intent>
  <pwg:ScanRegions>
    <pwg:ScanRegion>
      <pwg:Height>%d</pwg:Height>
      <pwg:ContentRegionUnits>escl:ThreeHundredthsOfInches</pwg:ContentRegionUnits>
      <pwg:Width>%d</pwg:Width>
      <pwg:XOffset>0</pwg:XOffset>
      <pwg:YOffset>0</pwg:YOffset>
    </pwg:ScanRegion>
  </pwg:ScanRegions>
  <pwg:InputSource>%s</pwg:InputSource>
  <scan:ColorMode>%s</scan:ColorMode>
  <scan:XResolution>%d</scan:XResolution>
  <scan:YResolution>%d</scan:YResolution>
  <pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>
  <scan:DocumentFormatExt>image/jpeg</scan:DocumentFormatExt>
</scan:ScanSettings>
`, xmlEscape(version), h, w, source, cfg.Scanner.ColorMode, resolution, resolution))
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Erreurs que l'opérateur peut corriger lui-même.
var (
	errScannerBusy = errors.New("scanner occupé (une copie ou une numérisation est en cours sur l'imprimante) : réessayer dans un instant")
	errAdfEmpty    = errors.New("chargeur vide : poser les feuilles dans le chargeur ou sur la vitre")
	errNoPage      = errors.New("le scanner n'a renvoyé aucune page")
)

// scannerPoll est l'attente entre deux demandes de page pendant que le scanner travaille.
var scannerPoll = time.Second

// scanJob numérise depuis source ("Platen" ou "Feeder") et renvoie les pages JPEG dans l'ordre.
func scanJob(c capabilities, source string, onPage func(int)) ([][]byte, error) {
	var location string
	for attempt := 0; ; attempt++ {
		response, err := scannerHTTP().Post(cfg.Scanner.URL+"/ScanJobs", "text/xml", bytes.NewReader(scanSettings(c, source)))
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if response.StatusCode == http.StatusCreated {
			location = response.Header.Get("Location")
			break
		}
		switch {
		case response.StatusCode == http.StatusServiceUnavailable && attempt < 5:
			time.Sleep(scannerPoll * 2)
			continue
		case response.StatusCode == http.StatusServiceUnavailable:
			return nil, errScannerBusy
		case response.StatusCode == http.StatusConflict && source == "Feeder":
			return nil, errAdfEmpty
		}
		return nil, fmt.Errorf("le scanner refuse la numérisation (HTTP %d) %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	job, err := resolveJob(location)
	if err != nil {
		return nil, err
	}
	var pages [][]byte
	waited := time.Duration(0)
	for len(pages) < 500 {
		response, err := scannerHTTP().Get(job + "/NextDocument")
		if err != nil {
			return pages, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
		response.Body.Close()
		switch {
		case response.StatusCode == http.StatusOK && readErr == nil && len(data) > 0:
			pages = append(pages, data)
			waited = 0
			if onPage != nil {
				onPage(len(pages))
			}
			continue
		case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
			// Plus de page : le travail est terminé.
		case response.StatusCode == http.StatusServiceUnavailable && waited < 2*time.Minute:
			time.Sleep(scannerPoll)
			waited += scannerPoll
			continue
		default:
			return pages, fmt.Errorf("lecture de la page %d impossible (HTTP %d)", len(pages)+1, response.StatusCode)
		}
		break
	}
	if len(pages) == 0 {
		if source == "Feeder" {
			return nil, errAdfEmpty
		}
		return nil, errNoPage
	}
	return pages, nil
}

// resolveJob transforme l'en-tête Location (absolu ou relatif) en adresse du travail.
func resolveJob(location string) (string, error) {
	if location == "" {
		return "", errors.New("le scanner n'a pas indiqué l'adresse du travail")
	}
	base, err := url.Parse(cfg.Scanner.URL + "/")
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	job := base.ResolveReference(ref)
	// Certains modèles répondent avec leur adresse IP interne : on garde l'hôte configuré.
	job.Scheme, job.Host = base.Scheme, base.Host
	return strings.TrimRight(job.String(), "/"), nil
}

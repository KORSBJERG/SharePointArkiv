package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const desktopAvailable = true

const (
	idSource = 100 + iota
	idSite
	idLibrary
	idFolder
	idTenant
	idClient
	idDrive
	idInterval
	idQuiet
	idDelete
	idBrowse
	idCertificate
	idShowCertificate
	idSave
	idCheck
	idTry
	idStart
	idLog
	idHelp
)

type desktopEvent struct {
	text     string
	finished bool
}

type desktop struct {
	window, instance                         uintptr
	font, heading, titleFont                 uintptr
	ownFont, ownHeading, ownTitleFont        bool
	dpi                                      int32
	executable, filename                     string
	controls                                 map[uintptr]uintptr
	status                                   uintptr
	events                                   chan desktopEvent
	cancel                                   context.CancelFunc
	closing, stopRequested, unreadableConfig bool
}

var currentDesktop *desktop
var desktopCallback = syscall.NewCallback(desktopProcedure)

func openDesktop(executable, filename string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setProcessDPIAware.Call()
	hresult, _, _ := coInitializeEx.Call(0, 2)
	if int32(hresult) >= 0 {
		defer coUninitialize.Call()
	}

	const className = "SharePointArkivDesktop"
	instance, _, _ := getModuleHandle.Call(0)
	cursor, _, _ := loadCursor.Call(0, 32512)
	icon, _, _ := loadIcon.Call(0, 32512)
	class := windowClass{
		size: uint32(unsafe.Sizeof(windowClass{})), windowProc: desktopCallback, instance: instance,
		icon: icon, cursor: cursor, background: 6, name: wide(className), smallIcon: icon,
	}
	if id, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); id == 0 {
		return fmt.Errorf("registrer appvinduet: %w", err)
	}
	const exStyle = wsExControlParent
	const style = wsCaption | wsSysMenu | wsMinimizeBox
	frame, dpi := centerWindow(800, 672, style, exStyle)
	window, err := createDesktopWindow(instance, className, "SharePoint Arkiv", exStyle, style, frame, 0, 0)
	if err != nil {
		return err
	}
	d := &desktop{
		window: window, instance: instance, dpi: dpi, executable: executable, filename: filename,
		controls: make(map[uintptr]uintptr), events: make(chan desktopEvent, 24),
	}
	currentDesktop = d
	defer func() { currentDesktop = nil }()
	d.font, d.ownFont = newUIFont(int(d.scale(15)), 400)
	d.heading, d.ownHeading = newUIFont(int(d.scale(17)), 600)
	d.titleFont, d.ownTitleFont = newUIFont(int(d.scale(29)), 600)
	defer d.freeFonts()
	if err := d.build(); err != nil {
		destroyWindow.Call(window)
		return err
	}
	cfg, err := readConfig(filename)
	if err != nil {
		cfg = defaultConfig()
		d.unreadableConfig = !errors.Is(err, os.ErrNotExist)
	}
	d.fill(cfg)
	d.updateControls()
	if d.unreadableConfig {
		d.show("De gemte indstillinger kunne ikke læses. " + err.Error())
	} else if err != nil {
		d.show("Vælg mapperne, og udfyld oplysningerne fra Microsoft Entra.")
	} else {
		d.show("Indstillingerne er indlæst. Du kan teste forbindelsen eller starte overvågning.")
	}
	showWindow.Call(window, 1)
	setFocus.Call(d.controls[idSource])
	var msg windowMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(result) {
		case 0:
			return nil
		case -1:
			return err
		}
		if handled, _, _ := isDialogMessage.Call(window, uintptr(unsafe.Pointer(&msg))); handled == 0 {
			translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}
}

func (d *desktop) freeFonts() {
	if d.ownTitleFont {
		deleteObject.Call(d.titleFont)
	}
	if d.ownHeading {
		deleteObject.Call(d.heading)
	}
	if d.ownFont {
		deleteObject.Call(d.font)
	}
}

func desktopProcedure(window, message, wParam, lParam uintptr) uintptr {
	d := currentDesktop
	if d != nil && d.window == window {
		switch message {
		case wmCommand:
			if lParam != 0 && wParam>>16&0xffff == 0 {
				d.command(wParam & 0xffff)
			}
			return 0
		case wmAppWork:
			d.readEvents()
			return 0
		case wmClose:
			if d.cancel == nil {
				destroyWindow.Call(window)
			} else if d.closing || confirm(window, "Vil du stoppe den aktive kørsel og lukke vinduet?") {
				if d.cancel != nil {
					d.closing = true
					d.stop()
				} else {
					destroyWindow.Call(window)
				}
			}
			return 0
		case wmControlColorStatic, wmControlColorButton:
			if lParam != d.status {
				return useWindowColors(wParam)
			}
		case wmDestroy:
			postQuitMessage.Call(0)
			return 0
		}
	}
	result, _, _ := defaultWindowProc.Call(window, message, wParam, lParam)
	return result
}

func (d *desktop) scale(value int32) int32 {
	return (value*d.dpi + 48) / 96
}

func (d *desktop) build() error {
	var firstError error
	control := func(id uintptr, class, text string, exStyle, style, font uintptr, x, y, width, height int32) uintptr {
		if firstError != nil {
			return 0
		}
		area := rectangle{d.scale(x), d.scale(y), d.scale(x + width), d.scale(y + height)}
		handle, err := createDesktopWindow(d.instance, class, text, exStyle, wsChild|wsVisible|style, area, d.window, id)
		if err != nil {
			firstError = err
			return 0
		}
		if id != 0 {
			d.controls[id] = handle
		}
		sendMessage.Call(handle, wmSetFont, font, 0)
		return handle
	}
	label := func(text string, x, y, width int32) {
		control(0, "STATIC", text, 0, 0, d.font, x, y, width, 21)
	}
	heading := func(text string, x, y, width int32) {
		control(0, "STATIC", text, 0, 0, d.heading, x, y, width, 25)
	}
	button := func(id uintptr, text string, x, y, width int32) {
		control(id, "BUTTON", text, 0, wsTabStop, d.font, x, y, width, 31)
	}
	edit := func(id uintptr, name, cue string, x, y, width int32, digits bool) {
		label(name, x, y, width)
		style := uintptr(wsTabStop | esAutoHScroll)
		if digits {
			style |= esNumber
		}
		handle := control(id, "EDIT", "", wsExClientEdge, style, d.font, x, y+22, width, 28)
		if handle != 0 && cue != "" {
			sendMessage.Call(handle, emSetCueBanner, 0, uintptr(unsafe.Pointer(wide(cue))))
		}
	}

	control(0, "STATIC", "SharePoint Arkiv", 0, 0, d.titleFont, 24, 16, 455, 39)
	label("Kopiér lokale filer til SharePoint Online.", 24, 59, 480)

	heading("Mapper", 24, 95, 350)
	edit(idSource, "Lokal kildemappe", "", 24, 129, 260, false)
	button(idBrowse, "Gennemse…", 292, 150, 99)
	edit(idSite, "SharePoint-websted", "https://firma.sharepoint.com/sites/Drift", 24, 187, 367, false)
	edit(idLibrary, "Dokumentbibliotek", "Fx Dokumenter", 24, 245, 367, false)
	edit(idFolder, "Mappe i biblioteket", "Fx Arkiv/Eksport · tomt felt for bibliotekets rod", 24, 303, 367, false)

	heading("Adgang til SharePoint", 424, 95, 352)
	edit(idTenant, "Lejer-id (Tenant ID)", "", 424, 129, 352, false)
	edit(idClient, "Program-id (Client ID)", "", 424, 187, 352, false)
	edit(idDrive, "Biblioteks-id (valgfrit)", "Graph-id, kan bruges i stedet for bibliotekets navn", 424, 245, 352, false)
	button(idCertificate, "Opret certifikat", 424, 319, 136)
	button(idShowCertificate, "Vis certifikat", 568, 319, 122)

	heading("Overvågning", 24, 385, 350)
	edit(idInterval, "Interval i minutter", "", 24, 417, 164, true)
	edit(idQuiet, "Ventetid efter ændring (sek.)", "", 209, 417, 213, true)
	control(idDelete, "BUTTON", "Slet verificerede lokale filer efter 30 døgn", 0,
		wsTabStop|bsAutoCheckBox, d.font, 424, 439, 352, 28)

	button(idSave, "Gem indstillinger", 24, 492, 140)
	button(idCheck, "Test forbindelse", 172, 492, 132)
	button(idTry, "Prøvekør kopiering", 312, 492, 143)
	button(idStart, "Start overvågning", 602, 492, 174)
	label("Status", 24, 545, 120)
	d.status = control(0, "EDIT", "", wsExClientEdge,
		wsTabStop|wsVScroll|esReadOnly|esMultiline|esAutoVScroll, d.font, 24, 568, 752, 46)
	label("Overvågningen stopper, når vinduet lukkes.", 24, 624, 478)
	button(idHelp, "Vejledning", 568, 620, 101)
	button(idLog, "Åbn log", 677, 620, 99)
	return firstError
}

func (d *desktop) fill(cfg config) {
	for id, value := range map[uintptr]string{
		idSource: cfg.LocalFolder, idSite: cfg.SiteURL, idLibrary: cfg.DocumentLibrary,
		idFolder: cfg.DestinationFolder, idTenant: cfg.TenantID, idClient: cfg.ClientID, idDrive: cfg.DriveID,
		idInterval: strconv.Itoa(cfg.IntervalMinutes), idQuiet: strconv.Itoa(cfg.MinimumFileAgeSeconds),
	} {
		setText(d.controls[id], value)
	}
	if cfg.DeleteLocal {
		sendMessage.Call(d.controls[idDelete], bmSetCheck, 1, 0)
	}
}

func (d *desktop) values() (config, error) {
	text := func(id uintptr) string { return strings.TrimSpace(windowText(d.controls[id])) }
	interval, err := strconv.Atoi(text(idInterval))
	if err != nil {
		return config{}, errors.New("Indtast intervallet i hele minutter.")
	}
	quiet, err := strconv.Atoi(text(idQuiet))
	if err != nil {
		return config{}, errors.New("Indtast ventetiden i hele sekunder.")
	}
	deleteLocal, _, _ := sendMessage.Call(d.controls[idDelete], bmGetCheck, 0, 0)
	return validateConfig(config{
		LocalFolder: text(idSource), SiteURL: text(idSite), DocumentLibrary: text(idLibrary),
		DestinationFolder: text(idFolder), TenantID: text(idTenant), ClientID: text(idClient),
		DriveID: text(idDrive), IntervalMinutes: interval, MinimumFileAgeSeconds: quiet,
		DeleteLocal: deleteLocal == 1,
	}, d.filename, d.executable)
}

func (d *desktop) command(id uintptr) {
	dir := filepath.Dir(d.filename)
	switch id {
	case idBrowse:
		folder, err := chooseFolder(d.window, windowText(d.controls[idSource]))
		if err != nil {
			d.show(err.Error())
		} else if folder != "" {
			setText(d.controls[idSource], folder)
		}
	case idShowCertificate:
		path := filepath.Join(dir, certName)
		if err := shellOpen(d.window, "explorer.exe", "/select,\""+path+"\""); err != nil {
			d.show(err.Error())
		}
	case idHelp:
		d.openInNotepad(filepath.Join(filepath.Dir(d.executable), "README.md"))
	case idLog:
		d.openInNotepad(filepath.Join(dir, "arkiv.log"))
	case idCertificate:
		d.perform("Opretter certifikat …", func(_ context.Context, _ io.Writer) (string, error) {
			err := generateCertificate(dir)
			return "Certifikatet er oprettet. Brug Vis certifikat, og upload klient.cer til appregistreringen i Microsoft Entra.", err
		})
	case idSave, idCheck, idTry, idStart:
		if id == idStart && d.cancel != nil {
			d.stop()
			return
		}
		cfg, err := d.values()
		if err != nil {
			d.show(err.Error())
			return
		}
		switch id {
		case idSave:
			d.save(cfg)
		case idCheck:
			d.run(cfg, runCheck, "Tester forbindelse …", "")
		case idTry:
			cfg.DeleteLocal = false
			d.run(cfg, runOnce, "Prøvekører kopiering til SharePoint …", "Prøvekørsel færdig. Se arkiv.log for detaljer.")
		case idStart:
			if cfg.DeleteLocal && !confirm(d.window, "Start overvågning med lokal sletning? Filer, der er mere end 30 døgn gamle, kan slettes permanent efter kontrol af SharePoint-kopien.") {
				return
			}
			if d.save(cfg) {
				d.run(cfg, runWatch, "Starter overvågning …", "")
			}
		}
	}
}

func (d *desktop) openInNotepad(path string) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.show("Filen findes endnu ikke: " + filepath.Base(path))
		} else {
			d.show(err.Error())
		}
		return
	}
	if err := shellOpen(d.window, "notepad.exe", "\""+path+"\""); err != nil {
		d.show(err.Error())
	}
}

func (d *desktop) save(cfg config) bool {
	if d.unreadableConfig && !confirm(d.window, "De tidligere indstillinger kunne ikke læses. Vil du erstatte dem med indstillingerne i vinduet?") {
		return false
	}
	if err := saveConfig(d.filename, cfg); err != nil {
		d.show("Indstillingerne kunne ikke gemmes. " + err.Error())
		return false
	}
	d.unreadableConfig = false
	d.show("Indstillingerne er gemt.")
	return true
}

func (d *desktop) show(text string) {
	setText(d.status, text)
}

func (d *desktop) updateControls() {
	ready := d.cancel == nil
	for _, id := range []uintptr{idSource, idSite, idLibrary, idFolder, idTenant, idClient, idDrive,
		idInterval, idQuiet, idDelete, idBrowse, idSave, idCheck, idTry} {
		setEnabled(d.controls[id], ready)
	}
	dir := filepath.Dir(d.filename)
	_, certErr := os.Stat(filepath.Join(dir, certName))
	_, keyErr := os.Stat(filepath.Join(dir, keyName))
	setEnabled(d.controls[idCertificate], ready && errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist))
	setEnabled(d.controls[idShowCertificate], certErr == nil)
	if ready {
		setText(d.controls[idStart], "Start overvågning")
	} else {
		setText(d.controls[idStart], "Stop")
	}
	setEnabled(d.controls[idStart], !d.stopRequested)
}

func (d *desktop) run(cfg config, mode runMode, working, done string) {
	d.perform(working, func(ctx context.Context, output io.Writer) (string, error) {
		err := runArchive(ctx, cfg, filepath.Dir(d.filename), mode, output)
		return done, err
	})
}

func (d *desktop) perform(working string, action func(context.Context, io.Writer) (string, error)) {
	if d.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.updateControls()
	d.show(working)
	go func() {
		output := desktopLog{window: d.window, events: d.events}
		text, err := action(ctx, output)
		switch {
		case ctx.Err() != nil:
			text = "Kørslen er stoppet."
		case err != nil:
			text = "Fejl. " + err.Error()
		}
		d.events <- desktopEvent{text: text, finished: true}
		postMessage.Call(d.window, wmAppWork, 0, 0)
	}()
}

func (d *desktop) stop() {
	d.cancel()
	d.stopRequested = true
	d.updateControls()
	d.show("Stopper den aktive kørsel …")
}

func (d *desktop) readEvents() {
	for {
		select {
		case event := <-d.events:
			if event.finished {
				d.cancel()
				d.cancel = nil
				d.stopRequested = false
				d.updateControls()
				if d.closing {
					destroyWindow.Call(d.window)
					return
				}
				if event.text != "" {
					d.show(event.text)
				}
			} else if !d.stopRequested {
				d.show(event.text)
			}
		default:
			return
		}
	}
}

type desktopLog struct {
	window uintptr
	events chan<- desktopEvent
}

func (log desktopLog) Write(data []byte) (int, error) {
	select {
	case log.events <- desktopEvent{text: strings.TrimSpace(string(data))}:
		postMessage.Call(log.window, wmAppWork, 0, 0)
	default:
	}
	return len(data), nil
}

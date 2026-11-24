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
	idFollowLog
	idLiveLog
)

const (
	desktopWidth  = 800
	desktopHeight = 800
	brandHeight   = 80
	logTimer      = 1
	iconResource  = 2 // The resource compiler assigns ID 1 to the manifest.
)

var (
	brandBackground = rgb(17, 26, 29)
	brandForeground = rgb(240, 236, 230)
	brandAccent     = rgb(146, 201, 193)
)

type desktop struct {
	window, instance                              uintptr
	font, heading, titleFont, logFont             uintptr
	ownFont, ownHeading, ownTitleFont, ownLogFont bool
	brandBrush, brandIcon                         uintptr
	brandControls                                 map[uintptr]uintptr
	dpi                                           int32
	executable, filename                          string
	controls                                      map[uintptr]uintptr
	status, liveLog                               uintptr
	logTail                                       logTail
	lastLogView                                   string
	done                                          chan string
	cancel                                        context.CancelFunc
	closing, stopRequested, unreadableConfig      bool
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

	const className = "PeakNetSharePointArkivDesktop"
	instance, _, _ := getModuleHandle.Call(0)
	cursor, _, _ := loadCursor.Call(0, 32512)
	icon, _, _ := loadIcon.Call(instance, iconResource)
	if icon == 0 {
		icon, _, _ = loadIcon.Call(0, 32512)
	}
	class := windowClass{
		size: uint32(unsafe.Sizeof(windowClass{})), windowProc: desktopCallback, instance: instance,
		icon: icon, cursor: cursor, background: 6, name: wide(className), smallIcon: icon,
	}
	if id, _, err := registerClass.Call(uintptr(unsafe.Pointer(&class))); id == 0 {
		return fmt.Errorf("registrer appvinduet: %w", err)
	}
	const exStyle = wsExControlParent
	const style = wsCaption | wsSysMenu | wsMinimizeBox
	frame, dpi := centerWindow(desktopWidth, desktopHeight, style, exStyle)
	window, err := createDesktopWindow(instance, className, applicationName, exStyle, style, frame, 0, 0)
	if err != nil {
		return err
	}
	d := &desktop{
		window: window, instance: instance, dpi: dpi, executable: executable, filename: filename,
		controls: make(map[uintptr]uintptr), done: make(chan string, 1),
		brandControls: make(map[uintptr]uintptr),
		logTail:       logTail{path: filepath.Join(filepath.Dir(filename), archiveLogName)},
	}
	currentDesktop = d
	defer func() { currentDesktop = nil }()
	d.font, d.ownFont = newUIFont("Segoe UI", int(d.scale(15)), 400)
	d.heading, d.ownHeading = newUIFont("Segoe UI", int(d.scale(17)), 600)
	d.titleFont, d.ownTitleFont = newUIFont("Segoe UI", int(d.scale(26)), 600)
	d.logFont, d.ownLogFont = newUIFont("Consolas", int(d.scale(14)), 400)
	iconSize := uintptr(d.scale(32))
	d.brandIcon, _, _ = loadImage.Call(instance, iconResource, 1, iconSize, iconSize, 0x8000)
	if !highContrastEnabled() {
		d.brandBrush, _, _ = createSolidBrush.Call(brandBackground)
	}
	defer d.freeGraphics()
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
	sendMessage.Call(d.controls[idFollowLog], bmSetCheck, 1, 0)
	d.refreshLog()
	if timer, _, err := startWindowTimer.Call(window, logTimer, 1000, 0); timer == 0 {
		destroyWindow.Call(window)
		return fmt.Errorf("start live log: %w", err)
	}
	defer stopWindowTimer.Call(window, logTimer)
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

func (d *desktop) freeGraphics() {
	if d.brandBrush != 0 {
		deleteObject.Call(d.brandBrush)
	}
	if d.ownLogFont {
		deleteObject.Call(d.logFont)
	}
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
			select {
			case text := <-d.done:
				d.finishWork(text)
			default:
			}
			return 0
		case wmTimer:
			if wParam == logTimer {
				d.refreshLog()
				return 0
			}
		case wmEraseBackground:
			if d.brandBrush != 0 {
				defaultWindowProc.Call(window, message, wParam, lParam)
				band := rectangle{right: d.scale(desktopWidth), bottom: d.scale(brandHeight)}
				fillRect.Call(wParam, uintptr(unsafe.Pointer(&band)), d.brandBrush)
				return 1
			}
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
			if d.brandBrush != 0 {
				if foreground, ok := d.brandControls[lParam]; ok {
					setTextColor.Call(wParam, foreground)
					setBackgroundColor.Call(wParam, brandBackground)
					return d.brandBrush
				}
			}
			if lParam != d.status && lParam != d.liveLog {
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

	textX := int32(24)
	if d.brandIcon != 0 {
		mark := control(0, "STATIC", "", 0, ssIcon, d.font, 24, 24, 32, 32)
		d.brandControls[mark] = brandForeground
		sendMessage.Call(mark, stmSetIcon, d.brandIcon, 0)
		textX = 72
	}
	brandLabel := control(0, "STATIC", "PeakNet", 0, 0, d.heading, textX, 12, 320, 24)
	d.brandControls[brandLabel] = brandAccent
	productLabel := control(0, "STATIC", "SharePoint Arkiv", 0, 0, d.titleFont, textX, 34, 470, 37)
	d.brandControls[productLabel] = brandForeground
	address := control(0, "STATIC", "peaknet.dk", 0, ssRight, d.font, 648, 33, 128, 23)
	d.brandControls[address] = brandAccent

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
	d.status = control(0, "EDIT", "", wsExClientEdge,
		wsTabStop|esReadOnly|esMultiline, d.font, 24, 532, 752, 38)

	heading("Live log", 24, 585, 208)
	control(idFollowLog, "BUTTON", "Følg live", 0, wsTabStop|bsAutoCheckBox, d.font, 675, 582, 101, 28)
	d.liveLog = control(idLiveLog, "EDIT", "", wsExClientEdge,
		wsTabStop|wsVScroll|esReadOnly|esMultiline|esAutoVScroll, d.logFont, 24, 613, 752, 131)
	sendMessage.Call(d.liveLog, emSetLimitText, logViewBytes*2, 0)

	label("Overvågningen i appen stopper, når vinduet lukkes.", 24, 763, 478)
	button(idHelp, "Vejledning", 535, 754, 108)
	button(idLog, "Åbn logfil", 651, 754, 125)
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
		d.openInNotepad(d.logTail.path)
	case idFollowLog:
		d.refreshLog()
	case idCertificate:
		d.perform("Opretter certifikat …", func(_ context.Context) (string, error) {
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
			d.run(cfg, runCheck, "Tester forbindelse …", "Forbindelsen virker. Se destinationen i loggen.")
		case idTry:
			cfg.DeleteLocal = false
			d.run(cfg, runOnce, "Prøvekører kopiering til SharePoint …", "Prøvekørsel færdig.")
		case idStart:
			if cfg.DeleteLocal && !confirm(d.window, "Start overvågning med lokal sletning? Filer, der er mere end 30 døgn gamle, kan slettes permanent efter kontrol af SharePoint-kopien.") {
				return
			}
			if d.save(cfg) {
				d.run(cfg, runWatch, "Overvågning kører.", "")
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
	d.perform(working, func(ctx context.Context) (string, error) {
		err := runArchive(ctx, cfg, filepath.Dir(d.filename), mode, io.Discard)
		return done, err
	})
}

func (d *desktop) perform(working string, action func(context.Context) (string, error)) {
	if d.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.updateControls()
	d.show(working)
	go func() {
		text, err := action(ctx)
		switch {
		case ctx.Err() != nil:
			text = "Kørslen er stoppet."
		case err != nil:
			text = "Fejl. " + err.Error()
		}
		d.done <- text
		postMessage.Call(d.window, wmAppWork, 0, 0)
	}()
}

func (d *desktop) stop() {
	d.cancel()
	d.stopRequested = true
	d.updateControls()
	d.show("Stopper den aktive kørsel …")
}

func (d *desktop) finishWork(text string) {
	d.cancel()
	d.cancel = nil
	d.stopRequested = false
	d.updateControls()
	if d.closing {
		destroyWindow.Call(d.window)
		return
	}
	if text != "" {
		d.show(text)
	}
	d.refreshLog()
}

func (d *desktop) refreshLog() {
	if checked, _, _ := sendMessage.Call(d.controls[idFollowLog], bmGetCheck, 0, 0); checked != 1 {
		return
	}
	text, changed, err := d.logTail.read()
	switch {
	case errors.Is(err, os.ErrNotExist):
		text = "Loggen vises her, når en kørsel starter."
	case err != nil:
		text = "Loggen kunne ikke læses. " + err.Error()
	case !changed:
		return
	case text == "":
		text = "Loggen indeholder endnu ingen linjer."
	}
	if text == d.lastLogView {
		return
	}
	d.lastLogView = text
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	setText(d.liveLog, text)
	sendMessage.Call(d.liveLog, emSetSelection, 0x7fffffff, 0x7fffffff)
	sendMessage.Call(d.liveLog, emScrollCaret, 0, 0)
}

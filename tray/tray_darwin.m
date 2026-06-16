#import <Cocoa/Cocoa.h>

@interface TrayHandler : NSObject
- (void)onShow:(id)sender;
- (void)onQuit:(id)sender;
@end

extern void goShowCallback();
extern void goQuitCallback();

static NSStatusItem *statusItem;
static TrayHandler *handler;

@implementation TrayHandler
- (void)onShow:(id)sender { goShowCallback(); }
- (void)onQuit:(id)sender { goQuitCallback(); }
@end

void hideFromDock(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    });
}

void showInDock(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

void createTray(const char* title, const void* iconData, int iconLen) {
    dispatch_async(dispatch_get_main_queue(), ^{
        statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
        [statusItem retain];

        if (iconData != NULL && iconLen > 0) {
            NSData *data = [NSData dataWithBytes:iconData length:iconLen];
            NSImage *icon = [[NSImage alloc] initWithData:data];
            icon.size = NSMakeSize(18, 18);
            statusItem.button.image = icon;
        } else {
            statusItem.button.title = [NSString stringWithUTF8String:title];
        }

        NSMenu *menu = [[NSMenu alloc] init];
        [menu retain];
        handler = [[TrayHandler alloc] init];
        [handler retain];

        NSMenuItem *showItem = [[NSMenuItem alloc] initWithTitle:@"Show Lokihub"
                                                          action:@selector(onShow:)
                                                   keyEquivalent:@""];
        [showItem setTarget:handler];
        [menu addItem:showItem];
        [menu addItem:[NSMenuItem separatorItem]];

        NSMenuItem *quitItem = [[NSMenuItem alloc] initWithTitle:@"Quit"
                                                          action:@selector(onQuit:)
                                                   keyEquivalent:@""];
        [quitItem setTarget:handler];
        [menu addItem:quitItem];

        statusItem.menu = menu;
    });
}

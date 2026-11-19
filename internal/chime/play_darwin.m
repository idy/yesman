//go:build darwin && cgo

#import <AVFoundation/AVFoundation.h>
#import <Foundation/Foundation.h>
#import <time.h>

// Own the player and its PCM until the clip ends. This runs on the sound
// worker, without a window, a recording session, or a system notification.
int yesman_play_chime(const void *wav, size_t length) {
    @autoreleasepool {
        NSData *data = [NSData dataWithBytes:wav length:length];
        AVAudioPlayer *player = [[AVAudioPlayer alloc] initWithData:data error:NULL];
        if (![player play]) {
            [player release];
            return 0;
        }
        double duration = player.duration;
        double start = (double)clock_gettime_nsec_np(CLOCK_UPTIME_RAW) / 1e9;
        double elapsed;
        do {
            [NSThread sleepForTimeInterval:0.02];
            elapsed = (double)clock_gettime_nsec_np(CLOCK_UPTIME_RAW) / 1e9 - start;
        } while (player.playing && elapsed < duration + 1);
        BOOL finished = !player.playing && elapsed + 0.05 >= duration;
        [player stop];
        [player release];
        return finished;
    }
}

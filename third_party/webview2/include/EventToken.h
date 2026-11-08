// MinGW 8.1 没有 Windows SDK 的 EventToken.h，补一个最小替身。
// WebView2.h 只用到 EventRegistrationToken 这一个结构体（add_*/remove_* 的令牌）。
#ifndef __eventtoken_h__
#define __eventtoken_h__

typedef struct EventRegistrationToken {
  __int64 value;
} EventRegistrationToken;

#endif // __eventtoken_h__

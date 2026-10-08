#include <windows.h>
#include <iphlpapi.h>
#include <stdio.h>

// 定义 MIB_TCPROW2 结构体
// 新版 MinGW-w64 的 <iphlpapi.h> 已经通过 tcpmib.h 提供 MIB_TCPROW2 /
// MIB_TCPTABLE2（条件为 _WIN32_WINNT >= 0x0600），无条件重定义会直接编译失败。
// 仅当系统头没有提供时才使用本地兜底定义。
#if !defined(_INC_TCPMIB) || (_WIN32_WINNT < 0x0600)
typedef struct _MIB_TCPROW2 {
	DWORD dwState;
	DWORD dwLocalAddr;
	DWORD dwLocalPort;
	DWORD dwRemoteAddr;
	DWORD dwRemotePort;
	DWORD dwOwningPid;
	DWORD dwOffloadState;
} MIB_TCPROW2, *PMIB_TCPROW2;

// 定义 MIB_TCPTABLE2 结构体
typedef struct _MIB_TCPTABLE2 {
	DWORD dwNumEntries;
	MIB_TCPROW2 table[ANY_SIZE];
} MIB_TCPTABLE2, *PMIB_TCPTABLE2;
#endif

// 定义 GETEXTENDEDTABLE 函数指针类型
typedef DWORD(WINAPI* GETEXTENDEDTABLE)(PVOID, PDWORD, BOOL, ULONG, TCP_TABLE_CLASS, ULONG);

// 定义 SETTCPENTRY 函数指针类型
typedef DWORD(WINAPI* SETTCPENTRY)(PMIB_TCPROW);

// 定义 GetTcpTable2 函数的指针类型
// 新版 MinGW-w64 的 <iphlpapi.h> 已声明真正的 GetTcpTable2 函数，同名 typedef
// 会报 "redeclared as different kind of symbol"，因此加 Fn 后缀避开冲突。
typedef DWORD (WINAPI * GetTcpTable2Fn)(PMIB_TCPTABLE2 TcpTable, PULONG SizePointer, BOOL Order);

// 关闭 TCP 连接初始化
void closeTcpConnectionInit();

// 根据 PID 关闭 TCP 连接
void closeTcpConnectionByPid(DWORD pid, DWORD ulAf);

// 获取指定 TCP 地址和端口的 PID
int getTcpInfoPID(char* Addr, int SunnyProt);
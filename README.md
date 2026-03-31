# `SqlTurbo`--SQL执行工具

# 一、背景

​	在日常开发过程中，经常会遇到数据库服务器是云服务器，或者数据库服务器在异地的问题，在执行大批量SQL脚本时，如果使用数据库连接工具执行，每一个SQL都需要加上网络延迟，将会导致耗费很长时间。比如使用云服务器，网络延迟为30ms，执行一个大的SQL脚本例如共计10000个SQL语句，在只计算网络延迟时，将耗费300秒，即5分钟时间。

​	该工具思路很简单，先将SQL脚本上传到指定服务器上，在服务器本地执行SQL脚本，通过减少网络延迟的目的来达到减少SQL执行时间。



# 二、技术架构

1. 使用go语言
2. 使用 DDD 的模式进行开发，领域对象使用充血模型。
3. 所有文件格式均使用 UTF-8。
4. 每个文件、每个结构体、每个方法等均要详细备注，包括业务逻辑、方法内主要逻辑、以及使用场景等。
5. 所有备注、注释、异常提示、日志等均使用中文，在重要节点、关键节点多打印完善的日志。
6. 日志使用`log/slog`，区分`DEBUG`、`INFO`、`WARN`、`ERROR`
7. 使用终端展示，使用`BubbleTea`-现代 TUI 框架.Terminal UI（TUI）作为终端的GUI组件。
8. 支持同时对多个库进行执行SQL，每一个库一个协程处理，提高并发。
9. 在`data`文件夹中生成一个`history`文件，用于记录上一次执行哪些数据库ID。
10. 当前工具需要支持在windows终端、mac终端、linux终端中执行。
11. 期望配置文件`yaml`如下，按照配置文件进行开发

[配置文件](./data/application.yaml)





# 三、使用流程

1. 确保配置文件中的配置正确，如果没有配置，或者配置文件格式错误，直接报错。

2. 日志文件夹：`./logs/`。需要区分info日志和error日志。

3. 初始化需要获取当前用户的电脑IP、Mac地址、公网IP；如果有多个电脑IP，多个数据都需要获取到。然后按照如下格式存储在`./data/history`中，如果存在多个数据，则就保存多个，如果没有就只保存有的数据，格式如下：

   ```sh
   ipv4_1=192.168.10.2
   ipv4_2=192.168.7.12
   ipv6_1=fde2:8715:dbc6:c000:39:a860:9077:2382
   public_ipv4_1=xxx.xxx.xxx.xxx
   public_ipv4_2=xxx.xxx.xxx.xxx
   public_ipv6_1=xxx.xxx.xxx.xxx.xxx.xxx.xxx.xxx
   public_ipv6_2=xxx.xxx.xxx.xxx.xxx.xxx.xxx.xxx
   mac1=xxx.xxx.xxx.xxx
   mac2=xxx.xxx.xxx.xxx
   ```

   

4. 配置文件校验没有错误时，读取`./data/history`，查看上一次执行了哪些ID库，然后在页面上默认选择。如果没有匹配到，或者不存在`history`文件，则需要用户选择第一次执行哪些数据库。终端展示示例如下：

```sh
Space选择和取消，Enter执行，默认选择上一次执行的数据库
[ ] ALL
[ ] ALL MySQL
[ ] ALL Dm
[ ] ALL PG
[x] SGSP_DEV_V2
[x] SGSP_TEST
[x] SGSP_NM
[ ] SGSP_YL
```

​	如果选择`All`则所有都选择。如果有多个mysql，则展示ALL MySQL；如果有多个dm数据库，则展示ALL Dm；如果有多个PG数据库，则展示ALL PG；如果数据库超过2个，则展示ALL；顺序一定是ALL、ALL MySQL、ALL Dm、ALL PG、按顺序展示其他数据库。

4. 选择完成后，点击回车开始执行

5. 选择多个数据库时，每一个数据库一个协程执行，提高执行效率。

6. 每一个数据库，使用SFTP将当前文件夹中的work_path中，如果文件夹不存在，则创建文件夹。

7. 查看工作文件夹中是否存在`lock_`开头的文件，如果存在，则按照配置文件中的配置进行等待重试。

8. `wait_time`为当存在锁文件时，需要等待几秒再重新查看是否有锁。必须大于1，如果不配置或者为0，或者小于1，均为默认中1.单位秒

9. `retry_times`为当存在锁文件时，需要重试几次查看锁是否释放.必须大于0，如果不配置或者为0，或者小于0，均为默认值0，即不重试。

10. 如果锁在等待的时间内释放，或者没有锁，需要创建自己的锁，占用当前工作目录，防止其他终端使用。

11. 在工作目录中生成锁文件：`lock_20260321120328654`；格式为`lock_`加上当前时间精确到毫秒值，如示例就是2026年03月21日12:03:28.654生成的锁文件。锁文件中需要记录在应用启动时获取到的当前用户的所有ip信息，mac地址信息等，全部写入到这个锁文件中。

12. 锁文件生成后，删除当前文件文件夹中所有以`.sql`结尾的SQL脚本。

13. 将当前目录中所有`.sql`结尾的SQL脚本上传到指定工作目录中。

14. 上传需要执行的脚步后，还需要上传对于数据库的策略文件上传到制定数据库中。区分数据库上传，在目录`./data/db_profiles/dm`、`./data/db_profiles/mysql`。区分数据库上传。

15. 设置工作目录中执行sql_execute_xx.sh执行权限。`chmod 111 sql_execute_*.sh`.

16. 根据数据库不同，sh脚本名称不同：如`sql_execute_mysql.sh`、`sql_execute_dm.sh`

17. 开始执行 `sql_execute_xx.sh`；命令：`export PATH=$PATH:/opt/mysql/bin && ./sql_execute_mysql.sh -ip=192.168.7.97 -port=3306 -user=root -password=SGSPDEV1234 -schema=SGSPDEV_V2`；根据配置文件中的 `env_path`，先追加 PATH，再执行脚本。

18. 执行所有脚步后，工作目录会存在日志文件`info.log`，下载执行日志到`./logs`中。命名改为数据库ID_info.log。

19. 删除最开始创建的锁文件，释放锁。当前数据库执行完成。

20. 在开始运行时，终端中一直需要展示每个数据库的执行情况，需要实时更新。使用`BubbleTea`-现代 TUI 框架.Terminal UI（TUI）。

21. 终端展示格式为:

    ```sh
    ${数据库ID}......${当前数据库所处步骤}......${信息}......${百分比/执行时间}
    # 如：SGSP_NM..........脚本上传中............正在上传[2026022701_李四（全量数据脚本）.sql]......80%
    ```

    数据库ID：sql_turbo配置下的数据id

    当前数据库所处步骤：包含 初始化、获取锁、等待锁释放、锁创建中、删除历史脚本、脚本上传中、脚本执行中、日志下载中、锁释放中、完成

    初始化：包括开始到文件创建，获取当前ip和mac信息。

    信息：如锁的名称、等待锁的名称、正在上传的脚本名称、正在执行的脚本名称等

    百分比：只有文件上传和下载时才展示。在文件执行时，现在当前脚本执行的时间，单位秒。

22. 所有数据库都执行完成后，等待用户回车后退出程序。终端窗口是否关闭取决于启动它的终端环境。



# 四、构建

Bash构建命令：

```bash
# win
BUILD_TIME="$(date '+%Y-%m-%dT%H:%M:%S%z')" GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-X sqlturbo/internal/version.BuildTime=${BUILD_TIME}" -o ./sqlturbo.exe ./cmd/sqlturbo

# mac
BUILD_TIME="$(date '+%Y-%m-%dT%H:%M:%S%z')" GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-X sqlturbo/internal/version.BuildTime=${BUILD_TIME}" -o ./sqlturbo ./cmd/sqlturbo
```

cmd构建命令：

```cmd
powershell -NoProfile -Command "$env:GOCACHE=(Join-Path (Get-Location) '.gocache'); $env:GOMODCACHE=(Join-Path (Get-Location) '.gomodcache'); $env:GOOS='windows'; $env:GOARCH='amd64'; $buildTime=(Get-Date).ToString('yyyy-MM-ddTHH:mm:sszzz'); go build -trimpath -ldflags ('-X sqlturbo/internal/version.BuildTime=' + $buildTime) -o .\sqlturbo.exe .\cmd\sqlturbo"
```

使用方式：

```bash
./sqlturbo
```




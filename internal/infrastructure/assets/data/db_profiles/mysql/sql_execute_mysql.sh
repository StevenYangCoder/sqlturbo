#!/bin/bash

# 设置环境格式为utf-8
export LANG=en_US.UTF-8
export LC_ALL=en_US.UTF-8

ip=""
port=""
user=""
password=""
schema=""

# 执行日志文件
INFO_LOG="info.log"
# 清除执行日志文件
> "$INFO_LOG"

# 获取参数
for arg in "$@"
do
    case "$arg" in
        -ip=*)
            ip="${arg#*=}"
            ;;
        -port=*)
            port="${arg#*=}"
            ;;
        -user=*)
            user="${arg#*=}"
            ;;
        -password=*)
            password="${arg#*=}"
            ;;
        -schema=*)
            schema="${arg#*=}"
            ;;
        *)
            echo "无效参数: $arg" | tee -a "$INFO_LOG"
            echo "用法: ./sql_execute.sh -ip=ip -port=port -user=user -password=password -schema=schema" | tee -a "$INFO_LOG"
            exit 1
            ;;
    esac
done

if [ -z "$ip" ] || [ -z "$port" ] || [ -z "$user" ] || [ -z "$password" ] || [ -z "$schema" ]; then
    echo "无效参数" | tee -a "$INFO_LOG"
    echo "用法: ./sql_execute.sh -ip=ip -port=port -user=user -password=password -schema=schema" | tee -a "$INFO_LOG"
    exit 1
fi

echo "========================" | tee -a "$INFO_LOG"
echo "数据库连接信息：" | tee -a "$INFO_LOG"
echo "ip       : $ip" | tee -a "$INFO_LOG"
echo "port     : $port" | tee -a "$INFO_LOG"
echo "user     : $user" | tee -a "$INFO_LOG"
echo "password : ********" | tee -a "$INFO_LOG"
echo "schema   : $schema" | tee -a "$INFO_LOG"
echo "========================" | tee -a "$INFO_LOG"
echo "" | tee -a "$INFO_LOG"

# 生成 .my.cnf
> ".my.cnf"
echo "[client]" >> ".my.cnf"
echo "user=$user" >> ".my.cnf"
echo "password=$password" >> ".my.cnf"
echo "host=$ip" >> ".my.cnf"
echo "port=$port" >> ".my.cnf"
echo "default-character-set=utf8" >> ".my.cnf"

# 遍历目录下的所有 .sql 文件
echo "------------------开始执行脚本------------------" | tee -a "$INFO_LOG"
for sql_file in *.sql; do
    if [ "login.sql" = "$sql_file" ]; then
        echo "跳过login.sql" | tee -a "$INFO_LOG"
    elif [ -f "$sql_file" ]; then
        echo "正在执行: $sql_file" | tee -a "$INFO_LOG"

        first_line=$(head -n 1 "$sql_file")
        if [ "$first_line" != "USE $schema;" ]; then
            tmp_file=$(mktemp)
            echo "USE $schema;" > "$tmp_file"
            cat "$sql_file" >> "$tmp_file"
            mv "$tmp_file" "$sql_file"
        fi

        last_line=$(tail -n 1 "$sql_file")
        if [ "$last_line" != "exit" ]; then
            echo >> "$sql_file"
            echo "COMMIT;" >> "$sql_file"
        fi

        mysql --defaults-extra-file=./.my.cnf -s -N -f "$schema" < "$sql_file" >> "$INFO_LOG" 2>&1

        echo "------------------$sql_file 执行完成" >> "$INFO_LOG"
        echo "" >> "$INFO_LOG"
    else
        echo "跳过非文件: $sql_file" | tee -a "$INFO_LOG"
    fi
done

echo "------------------脚本执行完成------------------" | tee -a "$INFO_LOG"

/**
 * 编辑 Dockerfile 并生成新环境版本的对话框。
 *
 * 语义固定：提交的永远是新版本的内容，历史版本不可改写。前端在打开时从服务端读取当前
 * 版本的 Dockerfile，用户修改后通过 `POST /api/v1/environments/{type}/{name}/versions`
 * 追加新版本；服务端负责 Dockerfile 与上下文校验、版本号分配与内容一致时复用既有版本。
 */
import { Alert, Flex, Input, Modal, Typography } from "antd";
import { useEffect, useRef, useState } from "react";

import {
  createEnvironmentVersion,
  fetchEnvironmentDockerfile,
  type Environment,
  type EnvironmentVersion,
  type Host,
} from "@/lib/api";
import {
  describeEnvironmentError,
  environmentWriteBlockedReason,
} from "@/pages/settings/settings-environments";

export type EnvironmentVersionModalProps = {
  /** 目标环境；为空时不打开对话框 */
  environment?: Environment;
  /** 编辑来源版本：读取它的 Dockerfile 作为新版本初始内容 */
  source?: EnvironmentVersion;
  /** 目标执行节点 */
  host?: Host;
  onCancel: () => void;
  /** 新版本写入成功后回调，调用方据此重新读取真实持久化数据 */
  onCreated: (version: EnvironmentVersion) => void;
  open: boolean;
};

export function EnvironmentVersionModal({
  environment,
  host,
  onCancel,
  onCreated,
  open,
  source,
}: EnvironmentVersionModalProps) {
  const [content, setContent] = useState("");
  const [parentVersion, setParentVersion] = useState("");
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  /** 只接受最近一次读取的结果，避免快速切换来源版本时旧内容覆盖新内容 */
  const loadRef = useRef(0);

  const hostID = host?.id ?? "";
  const blockedReason = environmentWriteBlockedReason(host);

  useEffect(() => {
    if (!open || !environment || !source || !hostID) return;
    const requestID = loadRef.current + 1;
    loadRef.current = requestID;
    setContent("");
    setError(undefined);
    setNotice(undefined);
    setParentVersion(source.parent_version ?? "");
    setLoading(true);
    fetchEnvironmentDockerfile(hostID, environment.type, environment.name, source.number)
      .then(({ data }) => {
        if (loadRef.current !== requestID) return;
        setContent(data.content);
      })
      .catch((cause: unknown) => {
        if (loadRef.current !== requestID) return;
        // 读取失败时不给出可编辑的空内容：那会让用户以为新版本就是空 Dockerfile。
        setError(`读取来源版本 Dockerfile 失败：${describeEnvironmentError(cause)}`);
      })
      .finally(() => {
        if (loadRef.current !== requestID) return;
        setLoading(false);
      });
  }, [environment, hostID, open, source]);

  const handleClose = () => {
    loadRef.current += 1;
    setContent("");
    setError(undefined);
    setNotice(undefined);
    onCancel();
  };

  const handleSubmit = async () => {
    if (!environment || !hostID) return;
    setSubmitting(true);
    setError(undefined);
    setNotice(undefined);
    try {
      const { data } = await createEnvironmentVersion(environment.type, environment.name, {
        dockerfile: content,
        host_id: hostID,
        parent_version: parentVersion.trim() || undefined,
      });
      setNotice(`已保存版本 ${data.version}（构建状态：${data.build_status}）`);
      onCreated(data);
    } catch (cause) {
      setError(describeEnvironmentError(cause));
    } finally {
      setSubmitting(false);
    }
  };

  const isLanguage = environment?.type === "language";

  return (
    <Modal
      destroyOnHidden
      okButtonProps={{
        disabled: Boolean(blockedReason) || loading,
        loading: submitting,
      }}
      okText="保存为新版本"
      open={open}
      title="编辑 Dockerfile 并生成新版本"
      width={880}
      onCancel={handleClose}
      onOk={() => {
        if (notice) {
          handleClose();
          return;
        }
        void handleSubmit();
      }}
    >
      <Flex gap="middle" vertical>
        {blockedReason && <Alert showIcon description={blockedReason} type="warning" />}
        <Alert
          showIcon
          description={
            "保存只会追加新版本，历史版本与其构建结果不会被修改。" +
            "内容与某个已成功构建的版本完全一致时，服务端会复用该版本号，不产生重复版本。"
          }
          title="版本只追加"
          type="info"
        />
        <Flex gap="small" wrap>
          <Typography.Text type="secondary">
            {`目标环境: ${environment ? environment.name : "未选择"}`}
          </Typography.Text>
          <Typography.Text type="secondary">
            {`来源版本: ${source?.version ?? "未选择"}`}
          </Typography.Text>
        </Flex>
        {isLanguage && (
          <Flex gap={4} vertical>
            <Typography.Text>父基础环境版本</Typography.Text>
            <Input
              aria-label="父基础环境版本"
              disabled={Boolean(blockedReason) || submitting}
              placeholder="例如 v0001"
              value={parentVersion}
              onChange={(event) => setParentVersion(event.target.value)}
            />
            <Typography.Text type="secondary">
              语言环境必须依赖已构建成功的基础环境版本；留空将沿用来源版本的父版本。
            </Typography.Text>
          </Flex>
        )}
        <Flex gap={4} vertical>
          <Typography.Text>Dockerfile</Typography.Text>
          <Input.TextArea
            aria-label="Dockerfile"
            autoSize={{ maxRows: 22, minRows: 12 }}
            disabled={Boolean(blockedReason) || loading || submitting}
            placeholder={loading ? "正在读取来源版本 Dockerfile…" : "FROM …"}
            spellCheck={false}
            value={content}
            onChange={(event) => setContent(event.target.value)}
          />
        </Flex>
        {error && <Alert showIcon description={error} title="操作失败" type="error" />}
        {notice && <Alert showIcon description={notice} title="新版本已保存" type="success" />}
      </Flex>
    </Modal>
  );
}

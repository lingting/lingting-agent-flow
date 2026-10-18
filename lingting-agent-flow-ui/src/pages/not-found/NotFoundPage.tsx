import { HomeOutlined } from "@ant-design/icons";
import { Button } from "lingting-react-ui";
import { useRouter } from "lingting-react-ui";

import { ROOT_REDIRECT_TO } from "@/app/navigation";

import { Result } from "antd";

/**
 * 未知路径的兜底页面。
 *
 * 由 LRI 布局作为 `notFoundComponent` 渲染在侧边栏内容区内，因此这里不再包裹主题容器。
 */
export function NotFoundPage() {
  const { navigate } = useRouter();

  return (
    <Result
      extra={
        <Button
          icon={<HomeOutlined />}
          type="primary"
          onClick={() => void navigate(ROOT_REDIRECT_TO)}
        >
          返回首页
        </Button>
      }
      status="404"
      subTitle="您访问的地址不存在或已变更。"
      title="页面不存在"
    />
  );
}

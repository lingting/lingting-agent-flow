import { AppSidebarLayout, type User, UserStore } from "lingting-react-ui";

import { navigationRoutes, PRODUCT_TITLE, ROOT_REDIRECT_TO } from "@/app/navigation";
import { ApiConnectionStatus } from "@/components/api-connection-status";
import { ProductBrand } from "@/components/product-brand";
import { ApiConnectionProvider, useApiConnectionContext } from "@/lib/api/ApiConnectionContext";
import { NotFoundPage } from "@/pages/not-found";

// 当前步骤没有后端认证，始终返回同一个本地用户；不伪造角色与权限。
const fixedUser: User = {
  id: "local",
  nickname: PRODUCT_TITLE,
  organizations: [],
  permission: [],
  roles: [],
  tenantId: "local",
};

UserStore.initialize({
  getUser: async () => ({ type: "login", value: fixedUser }),
  logout: async () => ({ type: "login", value: fixedUser }),
});

/** 头部右侧的连接指示器，状态来自真实的健康检查 */
function ConnectionIndicator() {
  const { check, state } = useApiConnectionContext();
  return <ApiConnectionStatus state={state} onRetry={() => void check()} />;
}

/**
 * 应用外壳：在布局外层提供 API 连接状态，使头部指示器与页面共享同一份状态。
 */
export function AppShell() {
  return (
    <ApiConnectionProvider>
      <AppSidebarLayout
        baseItems={[<ProductBrand icon="/favicon.svg" key="product-brand" title={PRODUCT_TITLE} />]}
        headerRightItems={[<ConnectionIndicator key="api-connection" />]}
        logoutPosition="hidden"
        menuRoutes={navigationRoutes}
        notFoundComponent={NotFoundPage}
        rootRedirectTo={ROOT_REDIRECT_TO}
        title={PRODUCT_TITLE}
        userPosition="hidden"
      />
    </ApiConnectionProvider>
  );
}
